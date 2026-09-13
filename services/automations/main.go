package main

import (
	"context"
	"fmt"
	"log"
	"net/url"
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
	"github.com/alonsosss/corforce-email/services/automations/internal/adapters/contactsclient"
	handler "github.com/alonsosss/corforce-email/services/automations/internal/adapters/http"
	natsadapter "github.com/alonsosss/corforce-email/services/automations/internal/adapters/nats"
	"github.com/alonsosss/corforce-email/services/automations/internal/adapters/postgres"
	"github.com/alonsosss/corforce-email/services/automations/internal/adapters/templatesclient"
	"github.com/alonsosss/corforce-email/services/automations/internal/adapters/transactionalclient"
	"github.com/alonsosss/corforce-email/services/automations/internal/app"
	"github.com/alonsosss/corforce-email/services/automations/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	defaultPort = 8051
	defaultTick = 15 * time.Second
	// tenantConcurrency y tenantBudget acotan cada pasada del ejecutor: cuantas empresas a
	// la vez y cuanto puede durar cada una (varios pasos de hasta dos CallTimeout).
	tenantConcurrency = 4
	tenantBudget      = 4 * time.Minute
	// pruneInterval: cada cuanto se olvidan los eventos de disparo fuera de retencion.
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
		log.Fatal("INTERNAL_GATEWAY_TOKEN es obligatoria: sin ella transactional, contacts y templates rechazan las llamadas")
	}
	contactsURL := requiredEnv("CONTACTS_URL")
	transactionalURL := requiredEnv("TRANSACTIONAL_URL")
	templatesURL := requiredEnv("TEMPLATES_URL")
	publicBase, err := publicBaseURL(os.Getenv("PUBLIC_BASE_URL"))
	if err != nil {
		log.Fatalf("PUBLIC_BASE_URL: %v", err)
	}
	perDay, err := envInt("AUTOMATIONS_DOI_MAX_PER_DAY", 1, 1, 100)
	if err != nil {
		log.Fatal(err)
	}
	per30, err := envInt("AUTOMATIONS_DOI_MAX_PER_30D", 3, 1, 1000)
	if err != nil {
		log.Fatal(err)
	}
	limits := domain.DOILimits{PerDay: perDay, Per30Days: per30}
	if err := limits.Validate(); err != nil {
		log.Fatalf("AUTOMATIONS_DOI_MAX_PER_DAY y AUTOMATIONS_DOI_MAX_PER_30D: %v", err)
	}
	pauseAfter, err := envInt("AUTOMATIONS_PAUSE_AFTER_FAILURES", 20, 1, 100000)
	if err != nil {
		log.Fatal(err)
	}
	tick, err := envDuration("AUTOMATIONS_TICK", defaultTick, time.Second, 10*time.Minute)
	if err != nil {
		log.Fatal(err)
	}
	port, err := envInt("AUTOMATIONS_PORT", defaultPort, 1, 65535)
	if err != nil {
		log.Fatal(err)
	}

	// ctx gobierna los trabajos de fondo (ejecutor, rele de la outbox, consumidores): se
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
		Settings:   postgres.NewSettingsRepository(ctxPool),
		Deliveries: postgres.NewDeliveryRepository(ctxPool),
		Workflows:  postgres.NewWorkflowRepository(ctxPool),
		Runs:       postgres.NewRunRepository(ctxPool),
		Processed:  postgres.NewProcessedRepository(ctxPool),
		Tx:         ctxPool,
		Events:     postgres.NewOutboxPublisher(ctxPool),
		Sender:     transactionalclient.New(transactionalURL, internalToken),
		Contacts:   contactsclient.New(contactsURL, internalToken),
		Templates:  templatesclient.New(templatesURL, internalToken),
		Config:     app.Config{DOILimits: limits, PauseAfterFailures: pauseAfter, PublicBaseURL: publicBase},
		Logger:     logger,
	})

	// Sin NATS el API sigue sirviendo y el ejecutor avanza las ejecuciones ya creadas; lo
	// que se detiene es la entrada por eventos y el correo del doble opt-in, que se ponen
	// al dia al volver el bus (los consumidores son durables). Los eventos propios esperan
	// en la outbox.
	if bus, err := events.NewBus(cfg.NATS.URL, logger); err != nil {
		logger.Error("automations: NATS no disponible; sin consumidores ni rele de outbox", zap.Error(err))
	} else {
		defer bus.Close()
		if err := natsadapter.EnsureStreams(bus); err != nil {
			logger.Error("automations: no se pudieron asegurar los streams", zap.Error(err))
		}
		go outbox.RunForTenants(ctx, tenantDB, db.PoolFromCtx, bus, logger, outbox.Options{})
		consumers := natsadapter.NewConsumers(bus, uc, tenantDB, logger)
		consumers.Start(ctx)
		defer consumers.Stop()
	}

	go runExecutor(ctx, tenantDB, uc, tick, logger)

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
		r.Mount("/api/v1/automations", h.Routes())
	})

	srv := server.New(port, r, logger)
	if err := srv.Run(); err != nil {
		logger.Fatal("server error", zap.Error(err))
	}
}

// runExecutor recorre cada AUTOMATIONS_TICK las empresas activas. Todas las replicas lo
// corren: SKIP LOCKED y la reserva de cada ejecucion reparten el trabajo sin que dos hagan
// el mismo paso a la vez.
func runExecutor(ctx context.Context, tenantDB *db.TenantDB, uc *app.UseCase, tick time.Duration, logger *zap.Logger) {
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
				logger.Warn("automations: pasada del ejecutor incompleta", zap.String("tenant_id", tenantID), zap.Error(err))
			}
			if prune {
				if _, err := uc.PruneProcessedEvents(tctx, id); err != nil && tctx.Err() == nil {
					logger.Warn("automations: no se podaron los eventos procesados", zap.String("tenant_id", tenantID), zap.Error(err))
				}
			}
		})
		if err != nil {
			if ctx.Err() == nil {
				logger.Warn("automations: no se pudo listar las empresas", zap.Error(err))
			}
			continue
		}
		if prune {
			lastPrune = time.Now()
		}
	}
}

// publicBaseURL valida la URL publica de la plataforma, de la que cuelga el enlace de
// prueba con que se comprueba la plantilla del doble opt-in.
func publicBaseURL(raw string) (string, error) {
	s := strings.TrimRight(strings.TrimSpace(raw), "/")
	if s == "" {
		return "", fmt.Errorf("es obligatoria")
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("debe ser una URL absoluta http(s) sin query ni fragmento")
	}
	return s, nil
}

func requiredEnv(key string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		log.Fatalf("%s es obligatoria", key)
	}
	return v
}

func envInt(key string, def, min, max int) (int, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < min || n > max {
		return 0, fmt.Errorf("%s debe ser un entero entre %d y %d", key, min, max)
	}
	return n, nil
}

func envDuration(key string, def, min, max time.Duration) (time.Duration, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < min || d > max {
		return 0, fmt.Errorf("%s debe ser una duracion entre %s y %s", key, min, max)
	}
	return d, nil
}
