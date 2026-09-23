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
	"github.com/alonsosss/corforce-email/services/analytics/internal/adapters/schedulercli"
	"github.com/alonsosss/corforce-email/services/analytics/internal/app"
	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

const (
	defaultPort          = 8053
	defaultRetentionDays = 90
	maxRetentionDays     = 3650
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
	perms, err := authz.CheckerFromEnv()
	if err != nil {
		log.Fatal(err)
	}
	// La poda la despacha el scheduler y se cierra por su API interna: sin a quien
	// responder, una ejecucion despachada quedaria colgada hasta vencer por plazo.
	internalToken, err := middleware.InternalGatewayToken()
	if err != nil {
		log.Fatal(err)
	}
	schedulerURL, err := config.RequiredServiceURL("SCHEDULER_URL")
	if err != nil {
		log.Fatal(err)
	}

	// ctx gobierna los trabajos de fondo (consumidores): se cancela cuando el HTTP termina
	// de apagarse.
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
		Links:            postgres.NewLinks(ctxPool),
		Reports:          postgres.NewReports(ctxPool),
		MessageRetention: time.Duration(retentionDays) * 24 * time.Hour,
	})

	// Sin NATS el panel sigue respondiendo con lo ya agregado; la ingesta y la poda se
	// reanudan desde el ultimo ack de cada durable cuando el bus vuelva (reinicio del
	// servicio). La poda no corre sola: la despacha el scheduler, que la reintenta.
	if bus, err := events.NewBus(cfg.NATS.URL, logger); err != nil {
		logger.Warn("analytics: NATS no disponible; sin ingesta de eventos ni poda", zap.Error(err))
	} else {
		defer bus.Close()
		consumers := natsadapter.NewConsumers(bus, uc, tenantDB, schedulercli.New(schedulerURL, internalToken), logger)
		consumers.Start(ctx)
		defer consumers.Stop()
	}

	h := handler.NewHandler(uc, perms)

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
