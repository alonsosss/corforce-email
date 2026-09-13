package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/config"
	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/outbox"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/server"
	handler "github.com/alonsosss/corforce-email/services/billing/internal/adapters/http"
	natsadapter "github.com/alonsosss/corforce-email/services/billing/internal/adapters/nats"
	outboxadapter "github.com/alonsosss/corforce-email/services/billing/internal/adapters/outbox"
	"github.com/alonsosss/corforce-email/services/billing/internal/adapters/postgres"
	"github.com/alonsosss/corforce-email/services/billing/internal/app"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// periodSweepLockKey es el cerrojo de lider del barrido de periodos en el registro.
const periodSweepLockKey int64 = 0x62696c6c696e6701

const (
	defaultPort               = 8055
	defaultSweepInterval      = 15 * time.Minute
	minSweepInterval          = time.Minute
	defaultProcessedRetention = 30 * 24 * time.Hour
	// minProcessedRetention: olvidar un evento que JetStream aun puede reentregar (los
	// streams consumidos retienen 7 dias) permitiria contarlo dos veces.
	minProcessedRetention = 7 * 24 * time.Hour
	pruneInterval         = 24 * time.Hour
)

type settings struct {
	port               int
	business           app.Config
	sweepInterval      time.Duration
	processedRetention time.Duration
}

func main() {
	logger, _ := zap.NewProduction()
	defer logger.Sync()
	response.SetUnexpectedLogger(logger)

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	st, err := loadSettings()
	if err != nil {
		log.Fatalf("billing: %v", err)
	}

	// ctx gobierna los trabajos de fondo; se cancela cuando el HTTP termina de apagarse.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Servicio del registro: un solo pool, sin enrutado por empresa. La empresa sale del
	// contexto (InjectFromGateway) y cada consulta filtra por tenant_id.
	registry, err := db.NewPool(ctx, cfg.Postgres.DSN(), logger)
	if err != nil {
		log.Fatalf("connect db: %v", err)
	}
	defer registry.Close()

	store := postgres.NewStore(registry.Pool)
	uc := app.New(app.Deps{
		Plans:         postgres.NewPlanRepository(store),
		Subscriptions: postgres.NewSubscriptionRepository(store),
		Usage:         postgres.NewUsageRepository(store),
		Ledger:        postgres.NewLedger(store),
		Tx:            store,
		Events:        outboxadapter.NewPublisher(store),
		Logger:        logger,
		Config:        st.business,
	})

	if st.business.DefaultPlanCode == "" {
		logger.Warn("billing: BILLING_DEFAULT_PLAN_CODE vacio; las empresas nuevas no recibiran suscripcion")
	}
	if !st.business.Enforce {
		logger.Warn("billing: BILLING_ENFORCE desactivado; las empresas sin suscripcion conservan sus derechos")
	}

	// Sin NATS el servicio sigue respondiendo derechos y consultas; los eventos propios
	// esperan en la outbox y el consumo se reanuda cuando el bus vuelva (reinicio).
	if bus, err := events.NewBus(cfg.NATS.URL, logger); err != nil {
		logger.Warn("billing: NATS no disponible; sin rele de outbox ni contadores por eventos", zap.Error(err))
	} else {
		defer bus.Close()
		if err := bus.EnsureStream(outboxadapter.StreamName, []string{outboxadapter.StreamSubjects}); err != nil {
			logger.Warn("ensure stream BILLING", zap.Error(err))
		}
		go outbox.NewRelay(registry.Pool, bus, logger, outbox.Options{}).Run(ctx)

		ingest := natsadapter.NewIngestWorker(bus, uc, logger)
		ingest.Start(ctx)
		defer ingest.Stop()
	}

	go runPeriodSweeps(ctx, registry.Pool, uc, st.sweepInterval, logger)
	go runProcessedPrune(ctx, uc, st.processedRetention, logger)

	h := handler.NewHandler(uc, authz.NewCheckerFromEnv())

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RequireGatewayToken)
	r.Use(middleware.InjectFromGateway)
	r.Use(middleware.SecureHeaders)
	r.Use(middleware.Logger(logger))

	r.Group(func(r chi.Router) {
		r.Use(middleware.NewRateLimiter(120, time.Minute).Limit)
		r.Mount("/api/v1/billing", h.PublicRoutes())
	})
	// Interno: sin limite por IP, porque la consulta de derechos acompana a cada alta y a
	// cada envio y llega toda desde los mismos servicios.
	r.Mount("/internal/billing", h.InternalRoutes())

	srv := server.New(st.port, r, logger)
	if err := srv.Run(); err != nil {
		logger.Fatal("server error", zap.Error(err))
	}
}

func loadSettings() (settings, error) {
	st := settings{port: defaultPort, sweepInterval: defaultSweepInterval, processedRetention: defaultProcessedRetention}
	if v := os.Getenv("BILLING_PORT"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil || p < 1 || p > 65535 {
			return st, fmt.Errorf("BILLING_PORT no valido: %q", v)
		}
		st.port = p
	}
	st.business.DefaultPlanCode = strings.TrimSpace(os.Getenv("BILLING_DEFAULT_PLAN_CODE"))
	if v := os.Getenv("BILLING_TRIAL_DAYS"); v != "" {
		d, err := strconv.Atoi(v)
		if err != nil || d < 0 || d > 366 {
			return st, fmt.Errorf("BILLING_TRIAL_DAYS debe ser un entero entre 0 y 366: %q", v)
		}
		st.business.TrialDays = d
	}
	if v := os.Getenv("BILLING_ENFORCE"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return st, fmt.Errorf("BILLING_ENFORCE debe ser true o false: %q", v)
		}
		st.business.Enforce = b
	}
	if v := os.Getenv("BILLING_PERIOD_SWEEP_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < minSweepInterval {
			return st, fmt.Errorf("BILLING_PERIOD_SWEEP_INTERVAL debe ser una duracion de al menos %s: %q", minSweepInterval, v)
		}
		st.sweepInterval = d
	}
	if v := os.Getenv("BILLING_PROCESSED_EVENTS_RETENTION"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < minProcessedRetention {
			return st, fmt.Errorf("BILLING_PROCESSED_EVENTS_RETENTION debe ser una duracion de al menos %s: %q", minProcessedRetention, v)
		}
		st.processedRetention = d
	}
	return st, nil
}

// runPeriodSweeps cierra periodos en una sola replica a la vez (cerrojo de lider sobre el
// registro). Una pasada al arrancar cubre lo que vencio mientras el servicio no corria.
func runPeriodSweeps(ctx context.Context, pool *pgxpool.Pool, uc *app.UseCase, interval time.Duration, logger *zap.Logger) {
	sweep := func() {
		release, ok := db.TryLeaderLock(ctx, pool, periodSweepLockKey)
		if !ok {
			return
		}
		defer release()
		rep, err := uc.SweepPeriods(ctx)
		if err != nil && ctx.Err() == nil {
			logger.Error("billing: barrido de periodos interrumpido", zap.Error(err))
		}
		if rep.Closed > 0 || rep.Transitions > 0 || rep.Failed > 0 {
			logger.Info("billing: barrido de periodos",
				zap.Int("closed", rep.Closed), zap.Int("transitions", rep.Transitions), zap.Int("failed", rep.Failed))
		}
	}
	sweep()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sweep()
		}
	}
}

// runProcessedPrune poda a diario el rastro de deduplicacion. Borrar filas antiguas es
// idempotente: no necesita cerrojo de lider.
func runProcessedPrune(ctx context.Context, uc *app.UseCase, retention time.Duration, logger *zap.Logger) {
	prune := func() {
		n, err := uc.PruneProcessed(ctx, retention)
		if err != nil {
			if ctx.Err() == nil {
				logger.Warn("billing: no se pudo podar processed_events", zap.Error(err))
			}
			return
		}
		if n > 0 {
			logger.Info("billing: processed_events podado", zap.Int64("rows", n))
		}
	}
	prune()
	t := time.NewTicker(pruneInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			prune()
		}
	}
}
