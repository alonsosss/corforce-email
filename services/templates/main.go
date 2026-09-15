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
	"github.com/alonsosss/corforce-email/pkg/outbox"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/server"
	handler "github.com/alonsosss/corforce-email/services/templates/internal/adapters/http"
	outboxadapter "github.com/alonsosss/corforce-email/services/templates/internal/adapters/outbox"
	"github.com/alonsosss/corforce-email/services/templates/internal/adapters/postgres"
	"github.com/alonsosss/corforce-email/services/templates/internal/app"
	"github.com/alonsosss/corforce-email/services/templates/internal/render"
	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

const defaultPort = 8047

func main() {
	logger, _ := zap.NewProduction()
	defer logger.Sync()
	response.SetUnexpectedLogger(logger)

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	port, err := config.EnvInt("TEMPLATES_PORT", defaultPort, 1, config.MaxPort)
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	registryPool, err := db.NewPool(ctx, cfg.Postgres.DSN(), logger)
	if err != nil {
		log.Fatalf("connect db: %v", err)
	}
	defer registryPool.Close()

	// Enrutado por celda: el pool de cada empresa se abre contra el host de su celda.
	tenantDB, mgr := db.NewTenantRouting(registryPool.Pool, cfg.Postgres, logger)
	defer mgr.CloseAll()
	ctxPool := &db.ContextPool{}

	// Los eventos salen por la outbox de cada base de empresa; sin NATS el servicio
	// arranca igual y los eventos quedan encolados hasta que el rele pueda entregarlos.
	if bus, err := events.NewBus(cfg.NATS.URL, logger); err != nil {
		logger.Warn("templates: NATS no disponible, los eventos quedan en la outbox", zap.Error(err))
	} else {
		defer bus.Close()
		if err := bus.EnsureStream(outboxadapter.StreamName, []string{outboxadapter.StreamSubjects}); err != nil {
			logger.Warn("ensure stream "+outboxadapter.StreamName, zap.Error(err))
		}
		go outbox.RunForTenants(ctx, tenantDB, db.PoolFromCtx, bus, logger, outbox.Options{})
	}

	uc := app.New(app.Deps{
		Repo:     postgres.NewRepository(ctxPool),
		Tx:       ctxPool,
		Renderer: render.NewPort(),
		Events:   outboxadapter.NewPublisher(ctxPool),
		Logger:   logger,
	})
	h := handler.NewHandler(uc, authz.NewCheckerFromEnv())

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RequireGatewayToken)
	r.Use(middleware.SecureHeaders)
	r.Use(middleware.Logger(logger))

	// API publico: identidad y empresa del gateway, permiso por accion en el handler y
	// cupo por usuario (todo el trafico comparte la IP del gateway).
	r.Group(func(r chi.Router) {
		r.Use(middleware.InjectFromGateway)
		r.Use(db.TenantPoolMiddleware(tenantDB))
		r.Use(middleware.NewRateLimiter(120, time.Minute).LimitPerUser)
		r.Mount("/api/v1/templates", h.Routes())
	})

	// API interno para transactional y campaigns: un renderizado por envio, sin cupo. La
	// empresa viene en X-Tenant-ID y solo se acepta tras el token interno.
	r.Group(func(r chi.Router) {
		r.Use(middleware.InjectFromGateway)
		r.Use(db.TenantHeaderPoolMiddleware(tenantDB))
		r.Mount("/internal/templates", h.InternalRoutes())
	})

	srv := server.New(port, r, logger)
	if err := srv.Run(); err != nil {
		logger.Fatal("server error", zap.Error(err))
	}
}
