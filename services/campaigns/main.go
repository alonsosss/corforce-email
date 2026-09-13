package main

import (
	"context"
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
	"github.com/alonsosss/corforce-email/services/campaigns/internal/adapters/contactsclient"
	handler "github.com/alonsosss/corforce-email/services/campaigns/internal/adapters/http"
	natsadapter "github.com/alonsosss/corforce-email/services/campaigns/internal/adapters/nats"
	"github.com/alonsosss/corforce-email/services/campaigns/internal/adapters/postgres"
	"github.com/alonsosss/corforce-email/services/campaigns/internal/adapters/templatesclient"
	"github.com/alonsosss/corforce-email/services/campaigns/internal/adapters/transactionalclient"
	"github.com/alonsosss/corforce-email/services/campaigns/internal/app"
	"github.com/alonsosss/corforce-email/services/campaigns/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	defaultPort = 8052
	defaultTick = 15 * time.Second
	minTick     = time.Second
	// tenantConcurrency y tenantBudget acotan cada pasada del orquestador: cuantas
	// empresas a la vez y cuanto puede durar cada una (varios lotes de CallTimeout).
	tenantConcurrency = 4
	tenantBudget      = 4 * time.Minute
	// pruneInterval: cada cuanto se olvidan los eventos ya contados fuera de retencion.
	pruneInterval = time.Hour
)

func main() {
	logger, _ := zap.NewProduction()
	defer logger.Sync()
	response.SetUnexpectedLogger(logger)

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	internalToken := os.Getenv("INTERNAL_GATEWAY_TOKEN")
	if internalToken == "" {
		log.Fatal("INTERNAL_GATEWAY_TOKEN es obligatoria: sin ella contacts, transactional y templates rechazan las llamadas")
	}
	contactsURL := requiredEnv("CONTACTS_URL")
	transactionalURL := requiredEnv("TRANSACTIONAL_URL")
	templatesURL := requiredEnv("TEMPLATES_URL")

	batchSize := envInt("CAMPAIGNS_BATCH_SIZE", domain.MaxBatchSize)
	if batchSize > domain.MaxBatchSize {
		logger.Warn("CAMPAIGNS_BATCH_SIZE supera el tope del lote de transactional; se usa el tope",
			zap.Int("configured", batchSize), zap.Int("max", domain.MaxBatchSize))
		batchSize = domain.MaxBatchSize
	}
	tick := envDuration(logger, "CAMPAIGNS_TICK", defaultTick)

	// ctx gobierna los trabajos de fondo (orquestador, rele de la outbox, consumidor de
	// estadisticas): se cancela cuando el HTTP termina de apagarse.
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
		Campaigns: postgres.NewCampaignRepository(ctxPool),
		Batches:   postgres.NewBatchRepository(ctxPool),
		Stats:     postgres.NewStatsRepository(ctxPool),
		Tx:        ctxPool,
		Events:    postgres.NewOutboxPublisher(ctxPool),
		Audience:  contactsclient.New(contactsURL, internalToken),
		Sender:    transactionalclient.New(transactionalURL, internalToken),
		Templates: templatesclient.New(templatesURL, internalToken),
		Config:    app.Config{BatchSize: batchSize},
		Logger:    logger,
	})

	// Sin NATS las campanas se siguen entregando: sus eventos esperan en la outbox y las
	// estadisticas se ponen al dia cuando el bus vuelve (el consumidor es durable).
	if bus, err := events.NewBus(cfg.NATS.URL, logger); err != nil {
		logger.Error("campaigns: NATS no disponible; sin rele de outbox ni estadisticas", zap.Error(err))
	} else {
		defer bus.Close()
		if err := bus.EnsureStream("CAMPAIGNS", []string{"campaigns.>"}); err != nil {
			logger.Error("campaigns: no se pudo asegurar el stream CAMPAIGNS", zap.Error(err))
		}
		go outbox.RunForTenants(ctx, tenantDB, db.PoolFromCtx, bus, logger, outbox.Options{})
		stats := natsadapter.NewStatsConsumer(bus, uc, tenantDB, logger)
		stats.Start(ctx)
		defer stats.Stop()
	}

	go runOrchestrator(ctx, tenantDB, uc, tick, logger)

	h := handler.NewHandler(uc, authz.NewCheckerFromEnv())

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RequireGatewayToken)
	r.Use(middleware.InjectFromGateway)
	r.Use(middleware.SecureHeaders)
	r.Use(middleware.Logger(logger))
	// Todo entra por el gateway con sesion: la empresa sale del JWT (X-Tenant-ID). El
	// limite es por usuario porque detras del gateway todas las peticiones comparten IP.
	r.Group(func(r chi.Router) {
		r.Use(db.TenantPoolMiddleware(tenantDB))
		r.Use(middleware.NewRateLimiter(120, time.Minute).LimitPerUser)
		r.Mount("/api/v1/campaigns", h.Routes())
	})

	port := envInt("CAMPAIGNS_PORT", defaultPort)
	srv := server.New(port, r, logger)
	if err := srv.Run(); err != nil {
		logger.Fatal("server error", zap.Error(err))
	}
}

// runOrchestrator recorre cada CAMPAIGNS_TICK las empresas activas. Todas las replicas
// lo corren: el bloqueo SKIP LOCKED y la reserva de cada lote reparten el trabajo sin
// que dos procesen la misma campana a la vez.
func runOrchestrator(ctx context.Context, tenantDB *db.TenantDB, uc *app.UseCase, tick time.Duration, logger *zap.Logger) {
	t := time.NewTicker(tick)
	defer t.Stop()
	var lastPrune time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		prune := time.Since(lastPrune) >= pruneInterval
		err := tenantDB.ForEachActiveTenantConcurrent(ctx, tenantConcurrency, tenantBudget, func(tctx context.Context, tenantID string) {
			id, err := uuid.Parse(tenantID)
			if err != nil {
				return
			}
			if err := uc.Tick(tctx, id); err != nil && tctx.Err() == nil {
				logger.Warn("campaigns: pasada del orquestador incompleta", zap.String("tenant_id", tenantID), zap.Error(err))
			}
			if prune {
				if _, err := uc.PruneProcessedEvents(tctx, id); err != nil && tctx.Err() == nil {
					logger.Warn("campaigns: no se podaron los eventos contados", zap.String("tenant_id", tenantID), zap.Error(err))
				}
			}
		})
		if err != nil {
			if ctx.Err() == nil {
				logger.Warn("campaigns: no se pudo listar las empresas", zap.Error(err))
			}
			continue
		}
		if prune {
			lastPrune = time.Now()
		}
	}
}

func requiredEnv(key string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		log.Fatalf("%s es obligatoria", key)
	}
	return v
}

func envInt(key string, fallback int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key))); err == nil && v > 0 {
		return v
	}
	return fallback
}

func envDuration(logger *zap.Logger, key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		logger.Warn("duracion no valida; se usa el valor por defecto", zap.String("key", key), zap.String("value", raw))
		return fallback
	}
	if d < minTick {
		return minTick
	}
	return d
}
