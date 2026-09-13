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
	// La imagen es scratch y no trae la base de zonas horarias: sin esto, validar la
	// zona de un contacto (time.LoadLocation) fallaria para todas salvo UTC.
	_ "time/tzdata"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/config"
	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/outbox"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/server"
	handler "github.com/alonsosss/corforce-email/services/contacts/internal/adapters/http"
	natsadapter "github.com/alonsosss/corforce-email/services/contacts/internal/adapters/nats"
	outboxadapter "github.com/alonsosss/corforce-email/services/contacts/internal/adapters/outbox"
	"github.com/alonsosss/corforce-email/services/contacts/internal/adapters/postgres"
	"github.com/alonsosss/corforce-email/services/contacts/internal/adapters/suppressionclient"
	"github.com/alonsosss/corforce-email/services/contacts/internal/adapters/sweep"
	"github.com/alonsosss/corforce-email/services/contacts/internal/app"
	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

const (
	defaultPort = 8050
	// maxImportRows es el techo que admite CONTACTS_IMPORT_MAX_ROWS: el cuerpo de una
	// importacion se lee entero en memoria.
	maxImportRows = 200000
)

func main() {
	logger, _ := zap.NewProduction()
	defer logger.Sync()
	response.SetUnexpectedLogger(logger)

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	publicBase, err := absoluteURL(os.Getenv("PUBLIC_BASE_URL"))
	if err != nil {
		log.Fatalf("PUBLIC_BASE_URL (enlaces del doble opt-in): %v", err)
	}
	// El estado del contacto se decide con las causas vigentes que devuelve suppression:
	// sin su URL, los eventos de suppression no se podrian aplicar sin arriesgar un estado
	// que la contradiga.
	suppressionURL, err := absoluteURL(os.Getenv("SUPPRESSION_URL"))
	if err != nil {
		log.Fatalf("SUPPRESSION_URL (causas vigentes de suppression): %v", err)
	}
	doiTTL, err := envDuration("CONTACTS_DOI_TTL", app.DefaultDOITTL, time.Hour, 30*24*time.Hour)
	if err != nil {
		log.Fatal(err)
	}
	importMax, err := envInt("CONTACTS_IMPORT_MAX_ROWS", app.DefaultImportMaxRows, 1, maxImportRows)
	if err != nil {
		log.Fatal(err)
	}
	expiryEvery, err := envDuration("CONTACTS_EXPIRY_SWEEP_INTERVAL", sweep.DefaultExpiryInterval, time.Minute, 24*time.Hour)
	if err != nil {
		log.Fatal(err)
	}
	fullSweepAt, err := envDuration("CONTACTS_FULL_SWEEP_AT", sweep.DefaultFullAt, 0, 24*time.Hour)
	if err != nil {
		log.Fatal(err)
	}

	// ctx gobierna los trabajos de fondo (rele de la outbox, consumidor de suppression).
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
		Contacts:    postgres.NewContactRepository(ctxPool),
		Consents:    postgres.NewConsentRepository(ctxPool),
		Tokens:      postgres.NewTokenRepository(ctxPool),
		Lists:       postgres.NewListRepository(ctxPool),
		Attributes:  postgres.NewAttributeRepository(ctxPool),
		Segments:    postgres.NewSegmentRepository(ctxPool),
		Query:       postgres.NewSegmentQuery(ctxPool),
		Imports:     postgres.NewImportRepository(ctxPool),
		Tx:          ctxPool,
		Events:      outboxadapter.NewPublisher(ctxPool),
		Suppression: suppressionclient.New(suppressionURL, os.Getenv("INTERNAL_GATEWAY_TOKEN")),
		Config:      app.Config{PublicBaseURL: publicBase, DOITTL: doiTTL, ImportMaxRows: importMax},
		Logger:      logger,
	})

	// Sin NATS el servicio sigue sirviendo el API y la audiencia; los eventos propios
	// esperan en la outbox y los cambios de estado por suppression se reanudan cuando el
	// bus vuelva (reinicio del servicio).
	if bus, err := events.NewBus(cfg.NATS.URL, logger); err != nil {
		logger.Warn("contacts: NATS no disponible; sin rele de outbox ni consumo de suppression", zap.Error(err))
	} else {
		defer bus.Close()
		if err := bus.EnsureStream("CONTACTS", []string{"contacts.>"}); err != nil {
			logger.Warn("ensure stream CONTACTS", zap.Error(err))
		}
		go outbox.RunForTenants(ctx, tenantDB, db.PoolFromCtx, bus, logger, outbox.Options{})

		worker := natsadapter.NewSuppressionWorker(bus, uc, tenantDB, logger)
		worker.Start(ctx)
		defer worker.Stop()
	}

	// Sin evento de suppression cuando caduca una exclusion manual: el estado lo alcanza el
	// barrido. No depende de NATS: consulta suppression por HTTP y publica por la outbox.
	go sweep.New(registryPool.Pool, tenantDB, uc, logger, expiryEvery, fullSweepAt).Run(ctx)

	h := handler.NewHandler(handler.Deps{UC: uc, Perms: authz.NewCheckerFromEnv(), TenantDB: tenantDB, Logger: logger})

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RequireGatewayToken)
	r.Use(middleware.InjectFromGateway)
	r.Use(middleware.SecureHeaders)
	r.Use(middleware.Logger(logger))

	// Con sesion, por el gateway: la empresa sale del JWT (X-Tenant-ID).
	r.Group(func(r chi.Router) {
		r.Use(db.TenantPoolMiddleware(tenantDB))
		r.Use(middleware.NewRateLimiter(300, time.Minute).LimitPerUser)
		r.Mount("/api/v1/contacts", h.ContactRoutes())
		r.Mount("/api/v1/segments", h.SegmentRoutes())
	})
	// Publico, sin sesion: el enlace del doble opt-in. Limite por ip, que aqui es la
	// unica identidad (X-Real-IP del gateway).
	r.Group(func(r chi.Router) {
		r.Use(middleware.NewRateLimiter(30, time.Minute).Limit)
		r.Mount("/api/v1/public/contacts", h.PublicRoutes())
	})
	// Interno: campaigns pide la audiencia con el token interno y la empresa en la
	// cabecera. Sin limite por ip: todo llega del mismo servicio.
	r.Group(func(r chi.Router) {
		r.Use(db.TenantHeaderPoolMiddleware(tenantDB))
		r.Mount("/internal/contacts", h.InternalRoutes())
	})

	port, err := envInt("CONTACTS_PORT", defaultPort, 1, 65535)
	if err != nil {
		log.Fatal(err)
	}
	srv := server.New(port, r, logger)
	if err := srv.Run(); err != nil {
		logger.Fatal("server error", zap.Error(err))
	}
}

// absoluteURL valida una URL obligatoria de la configuracion (la publica del doble opt-in,
// la interna de suppression): el servicio no arranca a medias sin ella.
func absoluteURL(raw string) (string, error) {
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
