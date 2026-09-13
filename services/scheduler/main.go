package main

import (
	"context"
	_ "embed"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
	// La imagen es scratch y no trae la base de zonas horarias: sin esto, toda zona de un
	// trabajo distinta de UTC fallaria al validarla y al evaluar su calendario.
	_ "time/tzdata"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/config"
	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/outbox"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/server"
	catalogadapter "github.com/alonsosss/corforce-email/services/scheduler/internal/adapters/catalog"
	handler "github.com/alonsosss/corforce-email/services/scheduler/internal/adapters/http"
	outboxadapter "github.com/alonsosss/corforce-email/services/scheduler/internal/adapters/outbox"
	"github.com/alonsosss/corforce-email/services/scheduler/internal/adapters/postgres"
	"github.com/alonsosss/corforce-email/services/scheduler/internal/app"
	"github.com/alonsosss/corforce-email/services/scheduler/internal/domain"
	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

// handlers.json es el catalogo de manejadores por defecto, embebido en el binario;
// SCHEDULER_HANDLERS_FILE lo sustituye entero (no lo mezcla) para otro despliegue.
//
//go:embed handlers.json
var defaultHandlers []byte

// sweepLockKey es el cerrojo de lider del barrido de vencidas y reintentos en la base de
// cada empresa.
const sweepLockKey int64 = 0x7363686564737770 // "schedswp"

const (
	defaultPort  = 8033
	tickInterval = 30 * time.Second
)

// tzCheckFlag hace que el binario solo compruebe que carga zonas horarias y salga, sin
// abrir conexiones: el Dockerfile lo corre sobre la imagen final.
const tzCheckFlag = "--tzcheck"

func main() {
	if len(os.Args) > 1 && os.Args[1] == tzCheckFlag {
		if err := domain.CheckTZDatabase(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	logger, _ := zap.NewProduction()
	defer logger.Sync()
	response.SetUnexpectedLogger(logger)

	// Sin base de zonas cada cron fuera de UTC se desactivaria al vencer: mejor no arrancar.
	if err := domain.CheckTZDatabase(); err != nil {
		log.Fatalf("%v", err)
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	catalog, err := loadCatalog()
	if err != nil {
		log.Fatalf("%v", err)
	}
	retry := domain.RetryPolicy{
		BaseDelay: envDuration("SCHEDULER_RETRY_BASE_DELAY", 30*time.Second),
		MaxDelay:  envDuration("SCHEDULER_RETRY_MAX_DELAY", time.Hour),
	}
	if err := retry.Validate(); err != nil {
		log.Fatalf("SCHEDULER_RETRY_BASE_DELAY / SCHEDULER_RETRY_MAX_DELAY: %v", err)
	}

	registryPool, err := db.NewPool(ctx, cfg.Postgres.DSN(), logger)
	if err != nil {
		log.Fatalf("connect db: %v", err)
	}
	defer registryPool.Close()

	tenantDB, mgr := db.NewTenantRouting(registryPool.Pool, cfg.Postgres, logger)
	defer mgr.CloseAll()
	ctxPool := &db.ContextPool{}

	uc := app.NewSchedulerUseCase(app.SchedulerDeps{
		Jobs:       postgres.NewJobDefinitionRepo(ctxPool),
		Executions: postgres.NewJobExecutionRepo(ctxPool),
		Tasks:      postgres.NewScheduledTaskRepo(ctxPool),
		Schedules:  postgres.NewJobScheduleRepo(ctxPool),
		Events:     outboxadapter.NewPublisher(ctxPool),
		Tx:         postgres.NewTransactor(ctxPool),
		Catalog:    catalog,
		Retry:      retry,
		Logger:     logger,
	})
	logger.Info("scheduler: catalogo de manejadores cargado", zap.Int("manejadores", len(catalog.List())))

	// Sin NATS el scheduler sigue despachando y cerrando: los eventos esperan en la outbox
	// de cada empresa hasta que el bus vuelva.
	if bus, err := events.NewBus(cfg.NATS.URL, logger); err != nil {
		logger.Error("scheduler: NATS no disponible; los eventos quedan en la outbox", zap.Error(err))
	} else {
		defer bus.Close()
		if err := bus.EnsureStream(outboxadapter.StreamName, []string{outboxadapter.StreamSubjects}); err != nil {
			logger.Error("scheduler: no se pudo asegurar el stream", zap.Error(err))
		}
		go outbox.RunForTenants(ctx, tenantDB, db.PoolFromCtx, bus, logger, outbox.Options{})
	}

	go runTicker(ctx, tenantDB, uc, envInt("SCHEDULER_TENANT_CONCURRENCY", 4), envDuration("SCHEDULER_TENANT_TIMEOUT", 20*time.Second), logger)

	h := handler.NewHandler(handler.Deps{
		UC:    uc,
		Perms: authz.NewCheckerFromEnv(),
		API: []func(http.Handler) http.Handler{
			db.TenantPoolMiddleware(tenantDB),
			middleware.NewRateLimiter(60, time.Minute).Limit,
		},
		Internal: []func(http.Handler) http.Handler{
			db.TenantHeaderPoolMiddleware(tenantDB),
		},
	})

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RequireGatewayToken)
	r.Use(middleware.InjectFromGateway)
	r.Use(middleware.SecureHeaders)
	r.Use(middleware.Logger(logger))
	r.Mount("/", h.Routes())

	srv := server.New(envInt("SCHEDULER_PORT", defaultPort), r, logger)
	if err := srv.Run(); err != nil {
		logger.Fatal("server error", zap.Error(err))
	}
}

// runTicker recorre las empresas activas cada tickInterval.
//
// El barrido de tenants es concurrente y con timeout por tenant. En una plataforma
// multitenant el numero de bases crece con los clientes: recorrerlas en serie haria que el
// ciclo durase la suma de todas y, con un ticker de 30 s, los trabajos de las ultimas bases
// llegarian tarde en cuanto una base lenta o caida bloquease a las demas. Con el pool de
// trabajadores el ciclo dura aproximadamente lo que el lote mas lento, y el timeout aisla a
// cada tenant: una base que no responde pierde su turno, no el de todos.
//
// La primera vuelta que alcanza a cada empresa reconcilia antes sus calendarios cron, que
// hasta que el scheduler evaluo la expresion se escribian cada hora desde la ultima pasada.
// SQL no sabe evaluar una expresion cron, por eso no es una migracion. Se anota por proceso
// y no se repite; una empresa que falla lo reintenta en la vuelta siguiente.
func runTicker(ctx context.Context, tenantDB *db.TenantDB, uc *app.SchedulerUseCase, concurrency int, perTenant time.Duration, logger *zap.Logger) {
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	var reconciled sync.Map
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		err := tenantDB.ForEachActiveTenantConcurrent(ctx, concurrency, perTenant, func(tCtx context.Context, tenantID string) {
			if _, done := reconciled.Load(tenantID); !done {
				fixed, err := uc.ReconcileCronSchedules(tCtx)
				if err != nil {
					logger.Error("scheduler: no se reconciliaron los calendarios cron", zap.String("tenant_id", tenantID), zap.Error(err))
				} else {
					reconciled.Store(tenantID, struct{}{})
					if fixed > 0 {
						logger.Info("scheduler: calendarios cron reconciliados", zap.String("tenant_id", tenantID), zap.Int("corregidos", fixed))
					}
				}
			}
			uc.ProcessDueJobs(tCtx)
			uc.ProcessPendingTasks(tCtx)
			sweep(tCtx, uc)
		})
		if err != nil && ctx.Err() == nil {
			logger.Error("scheduler: list tenants", zap.Error(err))
		}
	}
}

// sweep vence y despacha reintentos en la base del contexto con el cerrojo de lider. Las
// filas ya se toman con SKIP LOCKED; el cerrojo evita ademas que todas las replicas
// recorran la misma base en la misma vuelta.
func sweep(ctx context.Context, uc *app.SchedulerUseCase) {
	pool, ok := db.PoolFromCtx(ctx)
	if !ok {
		return
	}
	release, ok := db.TryLeaderLock(ctx, pool, sweepLockKey)
	if !ok {
		return
	}
	defer release()
	uc.ExpireOverdue(ctx)
	uc.DispatchRetries(ctx)
}

func loadCatalog() (*domain.HandlerCatalog, error) {
	raw := defaultHandlers
	if path := os.Getenv("SCHEDULER_HANDLERS_FILE"); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("leer SCHEDULER_HANDLERS_FILE: %w", err)
		}
		raw = b
	}
	return catalogadapter.Parse(raw)
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
