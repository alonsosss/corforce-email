package main

import (
	"context"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/config"
	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/outbox"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/server"
	handler "github.com/alonsosss/corforce-email/services/suppression/internal/adapters/http"
	natsadapter "github.com/alonsosss/corforce-email/services/suppression/internal/adapters/nats"
	outboxadapter "github.com/alonsosss/corforce-email/services/suppression/internal/adapters/outbox"
	"github.com/alonsosss/corforce-email/services/suppression/internal/adapters/postgres"
	"github.com/alonsosss/corforce-email/services/suppression/internal/app"
	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

func main() {
	logger, _ := zap.NewProduction()
	defer logger.Sync()
	response.SetUnexpectedLogger(logger)

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	// ctx gobierna los trabajos de fondo (rele de la outbox, consumidor de eventos): se
	// cancela cuando el HTTP termina de apagarse.
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
		Entries: postgres.NewRepository(ctxPool),
		Imports: postgres.NewImportRepository(ctxPool),
		Tx:      ctxPool,
		Events:  outboxadapter.NewPublisher(ctxPool),
		Logger:  logger,
	})

	// Sin NATS el servicio sigue sirviendo la consulta previa al envio, que es lo
	// critico; los eventos propios esperan en la outbox y la ingesta de rebotes se
	// reanuda cuando el bus vuelva (reinicio del servicio).
	if bus, err := events.NewBus(cfg.NATS.URL, logger); err != nil {
		logger.Warn("suppression: NATS no disponible; sin rele de outbox ni ingesta de rebotes", zap.Error(err))
	} else {
		defer bus.Close()
		if err := bus.EnsureStream("SUPPRESSION", []string{"suppression.>"}); err != nil {
			logger.Warn("ensure stream SUPPRESSION", zap.Error(err))
		}
		go outbox.RunForTenants(ctx, tenantDB, db.PoolFromCtx, bus, logger, outbox.Options{})

		ingest := natsadapter.NewIngestWorker(bus, uc, tenantDB, logger)
		ingest.Start(ctx)
		defer ingest.Stop()
	}

	h := handler.NewHandler(uc, authz.NewCheckerFromEnv())

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RequireGatewayToken)
	r.Use(middleware.InjectFromGateway)
	r.Use(middleware.SecureHeaders)
	r.Use(middleware.Logger(logger))

	// Publico: por el gateway, con sesion; la empresa sale del JWT (X-Tenant-ID).
	r.Group(func(r chi.Router) {
		r.Use(db.TenantPoolMiddleware(tenantDB))
		r.Use(middleware.NewRateLimiter(120, time.Minute).Limit)
		r.Mount("/api/v1/suppression", h.PublicRoutes())
	})
	// Interno: servicio a servicio con el token interno; la empresa viaja en la
	// cabecera. Sin limite por IP: la consulta previa al envio llega toda desde el
	// mismo servicio y acompanaria a cada correo.
	r.Group(func(r chi.Router) {
		r.Use(db.TenantHeaderPoolMiddleware(tenantDB))
		r.Mount("/internal/suppression", h.InternalRoutes())
	})

	port := 8046
	if p := os.Getenv("SUPPRESSION_PORT"); p != "" {
		if v, err := strconv.Atoi(p); err == nil {
			port = v
		}
	}

	srv := server.New(port, r, logger)
	if err := srv.Run(); err != nil {
		logger.Fatal("server error", zap.Error(err))
	}
}
