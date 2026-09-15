// reputation responde a "puede esta empresa enviar N mensajes de esta clase ahora":
// combina la reputacion de envio de la empresa por clase (tasas de rebote y queja sobre
// una ventana movil), los limites de tasa por hora y por dia, y el derecho mensual de su
// plan en billing.
package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/config"
	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/outbox"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/server"
	"github.com/alonsosss/corforce-email/services/reputation/internal/adapters/billingcli"
	handler "github.com/alonsosss/corforce-email/services/reputation/internal/adapters/http"
	natsadapter "github.com/alonsosss/corforce-email/services/reputation/internal/adapters/nats"
	outboxadapter "github.com/alonsosss/corforce-email/services/reputation/internal/adapters/outbox"
	"github.com/alonsosss/corforce-email/services/reputation/internal/adapters/postgres"
	promadapter "github.com/alonsosss/corforce-email/services/reputation/internal/adapters/prometheus"
	redisadapter "github.com/alonsosss/corforce-email/services/reputation/internal/adapters/redis"
	"github.com/alonsosss/corforce-email/services/reputation/internal/adapters/sweep"
	"github.com/alonsosss/corforce-email/services/reputation/internal/adapters/tenants"
	"github.com/alonsosss/corforce-email/services/reputation/internal/app"
	"github.com/go-chi/chi/v5"
	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

const (
	// sweepAt: el barrido corre poco despues de la medianoche UTC, cuando la ventana movil
	// acaba de desplazarse un dia.
	sweepAt = 5 * time.Minute
	// tenantWorkers: empresas que se recorren a la vez en el barrido y en el listado de
	// plataforma.
	tenantWorkers = 8
)

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
		log.Fatalf("reputation: configuracion no valida: %v", err)
	}

	// ctx gobierna los trabajos de fondo (rele de la outbox, consumidores, barrido): se
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

	rdb, err := newRedis(ctx, cfg.Redis, logger)
	if err != nil {
		log.Fatalf("redis: %v", err)
	}
	defer rdb.Close()

	directory := tenants.NewDirectory(tenantDB, tenantWorkers)
	uc := app.New(app.Deps{
		Stats:   postgres.NewStatsRepository(ctxPool),
		States:  postgres.NewStateRepository(ctxPool),
		Limits:  postgres.NewLimitRepository(ctxPool),
		Tx:      ctxPool,
		Events:  outboxadapter.NewPublisher(ctxPool),
		Rate:    redisadapter.NewRateLimiter(rdb),
		Billing: billingcli.New(st.billingURL, os.Getenv("INTERNAL_GATEWAY_TOKEN")),
		Tenants: directory,
		Metrics: promadapter.New(),
		Policy:  st.policy,
		Logger:  logger,
	})

	// Sin NATS el servicio sigue autorizando envios, que es lo critico; los cambios de
	// estado esperan en la outbox y la ingesta se reanuda cuando el bus vuelva (reinicio).
	if bus, err := events.NewBus(cfg.NATS.URL, logger); err != nil {
		logger.Warn("reputation: NATS no disponible; sin rele de outbox ni ingesta de hechos de entrega", zap.Error(err))
	} else {
		defer bus.Close()
		if err := bus.EnsureStream("REPUTATION", []string{"reputation.>"}); err != nil {
			logger.Warn("ensure stream REPUTATION", zap.Error(err))
		}
		go outbox.RunForTenants(ctx, tenantDB, db.PoolFromCtx, bus, logger, outbox.Options{})

		consumer := natsadapter.NewConsumer(bus, uc, directory, logger)
		consumer.Start(ctx)
		defer consumer.Stop()
	}

	go sweep.New(registryPool.Pool, uc, logger, sweepAt).Run(ctx)

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
		r.Mount("/api/v1/reputation", h.PublicRoutes())
	})
	// Catalogo: por el gateway, con sesion y sin base de empresa.
	r.Group(func(r chi.Router) {
		r.Use(middleware.NewRateLimiter(120, time.Minute).Limit)
		r.Mount("/api/v1/reputation/meta", h.MetaRoutes())
	})
	// Plataforma: por el gateway, solo superadmin; la empresa es la de la ruta.
	r.Group(func(r chi.Router) {
		r.Use(middleware.NewRateLimiter(120, time.Minute).Limit)
		r.Mount("/api/v1/reputation/tenants", h.PlatformRoutes())
	})
	// Interno: servicio a servicio con el token interno; la empresa viaja en la cabecera.
	// Sin limite por IP: la autorizacion acompana a cada envio y llega toda desde los
	// mismos servicios.
	r.Group(func(r chi.Router) {
		r.Use(db.TenantHeaderPoolMiddleware(tenantDB))
		r.Mount("/internal/reputation", h.InternalRoutes())
	})

	srv := server.New(st.port, r, logger)
	if err := srv.Run(); err != nil {
		logger.Fatal("server error", zap.Error(err))
	}
}

// newRedis abre el cliente del Redis de la plataforma con tiempos cortos: la reserva de
// tasa va en el camino de cada envio y, si Redis no responde, se autoriza sin ella en vez
// de esperar. Un Redis caido al arrancar no impide el arranque; el cliente reconecta solo.
// Una configuracion TLS invalida, o ausente fuera de desarrollo, si lo impide.
func newRedis(ctx context.Context, rc config.RedisConfig, logger *zap.Logger) (*goredis.Client, error) {
	tlsCfg, err := rc.TLSConfig()
	if err != nil {
		return nil, err
	}
	rdb := goredis.NewClient(&goredis.Options{
		Addr:                  rc.Addr(),
		Password:              rc.Password,
		DB:                    0,
		TLSConfig:             tlsCfg,
		DialTimeout:           time.Second,
		ReadTimeout:           500 * time.Millisecond,
		WriteTimeout:          500 * time.Millisecond,
		PoolTimeout:           time.Second,
		ContextTimeoutEnabled: true,
	})
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		logger.Warn("reputation: Redis no disponible al arrancar; la tasa se autoriza sin reservar cupo hasta que vuelva", zap.Error(err))
	}
	return rdb, nil
}
