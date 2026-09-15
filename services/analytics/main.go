package main

import (
	"context"
	"log"
	"time"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/config"
	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/server"
	handler "github.com/alonsosss/corforce-email/services/analytics/internal/adapters/http"
	natsadapter "github.com/alonsosss/corforce-email/services/analytics/internal/adapters/nats"
	"github.com/alonsosss/corforce-email/services/analytics/internal/adapters/postgres"
	"github.com/alonsosss/corforce-email/services/analytics/internal/app"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	defaultPort          = 8053
	defaultRetentionDays = 90
	maxRetentionDays     = 3650

	// La poda corre al arrancar y despues una vez al dia, empresa por empresa.
	pruneInterval    = 24 * time.Hour
	pruneConcurrency = 4
	prunePerTenant   = 10 * time.Minute
)

func main() {
	logger, _ := zap.NewProduction()
	defer logger.Sync()
	response.SetUnexpectedLogger(logger)

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	retentionDays, err := config.EnvInt("ANALYTICS_MESSAGE_RETENTION_DAYS", defaultRetentionDays, 1, maxRetentionDays)
	if err != nil {
		log.Fatal(err)
	}
	port, err := config.EnvInt("ANALYTICS_PORT", defaultPort, 1, config.MaxPort)
	if err != nil {
		log.Fatal(err)
	}

	// ctx gobierna los trabajos de fondo (consumidores, poda): se cancela cuando el HTTP
	// termina de apagarse.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	registryPool, err := db.NewPool(ctx, cfg.Postgres.DSN(), logger)
	if err != nil {
		log.Fatalf("connect db: %v", err)
	}
	defer registryPool.Close()

	tenantDB, mgr := db.NewTenantRouting(registryPool.Pool, cfg.Postgres, logger)
	defer mgr.CloseAll()
	ctxPool := &db.ContextPool{}

	uc := app.New(app.Deps{
		Tx:               ctxPool,
		Ledger:           postgres.NewLedger(ctxPool),
		Facts:            postgres.NewFacts(ctxPool),
		Stats:            postgres.NewStats(ctxPool),
		Campaigns:        postgres.NewCampaigns(ctxPool),
		Reports:          postgres.NewReports(ctxPool),
		MessageRetention: time.Duration(retentionDays) * 24 * time.Hour,
	})

	// Sin NATS el panel sigue respondiendo con lo ya agregado; la ingesta se reanuda
	// desde el ultimo ack de cada durable cuando el bus vuelva (reinicio del servicio).
	if bus, err := events.NewBus(cfg.NATS.URL, logger); err != nil {
		logger.Warn("analytics: NATS no disponible; sin ingesta de eventos", zap.Error(err))
	} else {
		defer bus.Close()
		consumers := natsadapter.NewConsumers(bus, uc, tenantDB, logger)
		consumers.Start(ctx)
		defer consumers.Stop()
	}

	go runPruner(ctx, tenantDB, uc, logger)

	h := handler.NewHandler(uc, authz.NewCheckerFromEnv())

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RequireGatewayToken)
	r.Use(middleware.InjectFromGateway)
	r.Use(middleware.SecureHeaders)
	r.Use(middleware.Logger(logger))
	r.Use(db.TenantPoolMiddleware(tenantDB))
	r.Use(middleware.NewRateLimiter(120, time.Minute).Limit)
	r.Mount("/api/v1/analytics", h.Routes())

	srv := server.New(port, r, logger)
	if err := srv.Run(); err != nil {
		logger.Fatal("server error", zap.Error(err))
	}
}

// runPruner poda cada empresa activa: filas de mensaje sin actividad y ids de evento ya
// olvidables. Varias replicas pueden podar a la vez sin conflicto: los DELETE son
// idempotentes.
func runPruner(ctx context.Context, tenantDB *db.TenantDB, uc *app.UseCase, logger *zap.Logger) {
	t := time.NewTicker(pruneInterval)
	defer t.Stop()
	for {
		err := tenantDB.ForEachActiveTenantConcurrent(ctx, pruneConcurrency, prunePerTenant, func(tctx context.Context, tenantID string) {
			id, err := uuid.Parse(tenantID)
			if err != nil {
				return
			}
			res, err := uc.Prune(tctx, id)
			if err != nil {
				if ctx.Err() == nil {
					logger.Warn("analytics: poda incompleta", zap.String("tenant_id", tenantID), zap.Error(err))
				}
				return
			}
			if res.Messages > 0 || res.Events > 0 {
				logger.Info("analytics: poda", zap.String("tenant_id", tenantID),
					zap.Int64("messages", res.Messages), zap.Int64("events", res.Events))
			}
		})
		if err != nil && ctx.Err() == nil {
			logger.Warn("analytics: no se pudo listar las empresas para la poda", zap.Error(err))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
