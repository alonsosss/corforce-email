package main

import (
	"context"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/alonsosss/corforce-email/pkg/config"
	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/server"
	handler "github.com/alonsosss/corforce-email/services/scheduler/internal/adapters/http"
	natsadapter "github.com/alonsosss/corforce-email/services/scheduler/internal/adapters/nats"
	"github.com/alonsosss/corforce-email/services/scheduler/internal/adapters/postgres"
	"github.com/alonsosss/corforce-email/services/scheduler/internal/app"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

func main() {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	ctx := context.Background()

	registryPool, err := db.NewPool(ctx, cfg.Postgres.DSN(), logger)
	if err != nil {
		log.Fatalf("connect db: %v", err)
	}
	defer registryPool.Close()

	tenantDB, mgr := db.NewTenantRouting(registryPool.Pool, cfg.Postgres, logger)
	defer mgr.CloseAll()
	ctxPool := &db.ContextPool{}

	bus, err := events.NewBus(cfg.NATS.URL, logger)
	if err != nil {
		log.Fatalf("connect nats: %v", err)
	}
	defer bus.Close()

	jobRepo := postgres.NewJobDefinitionRepo(ctxPool)
	execRepo := postgres.NewJobExecutionRepo(ctxPool)
	taskRepo := postgres.NewScheduledTaskRepo(ctxPool)
	scheduleRepo := postgres.NewJobScheduleRepo(ctxPool)
	eventPub := natsadapter.NewEventPublisher(bus)

	uc := app.NewSchedulerUseCase(app.SchedulerDeps{
		Jobs:       jobRepo,
		Executions: execRepo,
		Tasks:      taskRepo,
		Schedules:  scheduleRepo,
		Events:     eventPub,
		Logger:     logger,
	})

	// El barrido de tenants es concurrente y con timeout por tenant. En una
	// plataforma multitenant el numero de bases crece con los clientes: recorrerlas
	// en serie haria que el ciclo durase la suma de todas y, con un ticker de 30 s,
	// los trabajos de las ultimas bases llegarian tarde en cuanto una base lenta
	// o caida bloquease a las demas. Con el pool de trabajadores el ciclo dura
	// aproximadamente lo que el lote mas lento, y el timeout aisla a cada tenant:
	// una base que no responde pierde su turno, no el de todos.
	tenantConcurrency := envInt("SCHEDULER_TENANT_CONCURRENCY", 4)
	tenantTimeout := envDuration("SCHEDULER_TENANT_TIMEOUT", 20*time.Second)
	lockerID := uuid.New().String()
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			if err := tenantDB.ForEachActiveTenantConcurrent(ctx, tenantConcurrency, tenantTimeout, func(tCtx context.Context, _ string) {
				uc.ProcessDueJobs(tCtx, lockerID)
				uc.ProcessPendingTasks(tCtx)
			}); err != nil {
				logger.Error("scheduler: list tenants", zap.Error(err))
			}
		}
	}()

	h := handler.NewHandler(uc)

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RequireGatewayToken)
	r.Use(middleware.InjectFromGateway)
	r.Use(db.TenantPoolMiddleware(tenantDB))
	r.Use(middleware.SecureHeaders)
	r.Use(middleware.Logger(logger))
	limiter := middleware.NewRateLimiter(60, time.Minute)
	r.Use(limiter.Limit)
	r.Mount("/", h.Routes())

	port := 8033
	if p := os.Getenv("SCHEDULER_PORT"); p != "" {
		if v, err := strconv.Atoi(p); err == nil {
			port = v
		}
	}

	srv := server.New(port, r, logger)
	if err := srv.Run(); err != nil {
		logger.Fatal("server error", zap.Error(err))
	}
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return def
}
