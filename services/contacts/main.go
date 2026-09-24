package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"log"
	"net/url"
	"os"
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
	"github.com/alonsosss/corforce-email/services/contacts/internal/adapters/formguard"
	handler "github.com/alonsosss/corforce-email/services/contacts/internal/adapters/http"
	natsadapter "github.com/alonsosss/corforce-email/services/contacts/internal/adapters/nats"
	outboxadapter "github.com/alonsosss/corforce-email/services/contacts/internal/adapters/outbox"
	"github.com/alonsosss/corforce-email/services/contacts/internal/adapters/postgres"
	"github.com/alonsosss/corforce-email/services/contacts/internal/adapters/prune"
	"github.com/alonsosss/corforce-email/services/contacts/internal/adapters/suppressionclient"
	"github.com/alonsosss/corforce-email/services/contacts/internal/adapters/sweep"
	"github.com/alonsosss/corforce-email/services/contacts/internal/app"
	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/alonsosss/corforce-email/services/contacts/internal/segment"
	"github.com/go-chi/chi/v5"
	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

const (
	defaultPort = 8050
	// maxImportRows es el techo que admite CONTACTS_IMPORT_MAX_ROWS: el cuerpo de una
	// importacion se lee entero en memoria.
	maxImportRows = 200000

	// Cupos por defecto de los envios publicos de formularios: una persona envia una vez; una
	// oficina o una red movil tras la misma IP, unas pocas.
	defaultFormSubmitsPerIP       = 10
	defaultFormIPWindow           = 10 * time.Minute
	defaultFormSubmitsPerFormHour = 300
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
	suppressionURL, err := config.RequiredServiceURL("SUPPRESSION_URL")
	if err != nil {
		log.Fatalf("causas vigentes de suppression: %v", err)
	}
	perms, err := authz.CheckerFromEnv()
	if err != nil {
		log.Fatal(err)
	}
	doiTTL, err := config.EnvDuration("CONTACTS_DOI_TTL", app.DefaultDOITTL, time.Hour, 30*24*time.Hour)
	if err != nil {
		log.Fatal(err)
	}
	importMax, err := config.EnvInt("CONTACTS_IMPORT_MAX_ROWS", app.DefaultImportMaxRows, 1, maxImportRows)
	if err != nil {
		log.Fatal(err)
	}
	fullSweepAt, err := config.EnvDuration("CONTACTS_FULL_SWEEP_AT", sweep.DefaultFullAt, 0, 24*time.Hour)
	if err != nil {
		log.Fatal(err)
	}
	// La retencion no baja del tope de las reglas de los ultimos N dias: una regla de 365
	// dias sobre 90 de historia diria que nadie abrio sin que sea cierto.
	retentionDays, err := config.EnvInt("CONTACTS_ENGAGEMENT_RETENTION_DAYS", domain.DefaultEngagementRetentionDays,
		segment.MaxLastDays, domain.MaxEngagementRetentionDays)
	if err != nil {
		log.Fatal(err)
	}
	port, err := config.EnvInt("CONTACTS_PORT", defaultPort, 1, config.MaxPort)
	if err != nil {
		log.Fatal(err)
	}
	forms, err := loadFormSettings()
	if err != nil {
		log.Fatal(err)
	}
	// Clave propia de los tokens de formulario, del almacen de secretos: derivarla del token
	// interno del gateway dejaria forjarlos a cualquier servicio que lo tenga, y rotar uno
	// invalidaria el otro.
	formTokens, err := app.NewFormTokenSigner(os.Getenv("CONTACTS_FORM_TOKEN_KEY"), rand.Reader)
	if err != nil {
		log.Fatalf("tokens de los formularios publicos: %v", err)
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

	rdb, err := newRedis(ctx, cfg.Redis, logger)
	if err != nil {
		log.Fatalf("redis: %v", err)
	}
	defer rdb.Close()
	formGuard := formguard.New(middleware.NewRedisRateLimitStore(rdb), formguard.Config{
		PerIP: forms.perIP, IPWindow: forms.ipWindow, PerFormHour: forms.perFormHour, TokenTTL: forms.tokenTTL,
	}, logger)

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
		Engagement:  postgres.NewEngagementRepository(ctxPool),
		Matcher:     postgres.NewSegmentQuery(ctxPool),
		Forms:       postgres.NewFormRepository(ctxPool),
		FormGuard:   formGuard,
		FormTokens:  formTokens,
		Config: app.Config{
			PublicBaseURL: publicBase, DOITTL: doiTTL, ImportMaxRows: importMax,
			EngagementRetention: time.Duration(retentionDays) * 24 * time.Hour,
			Forms:               app.FormConfig{MinFill: forms.minFill, TokenTTL: forms.tokenTTL, PlatformOrigin: app.OriginOf(publicBase)},
		},
		Logger: logger,
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
		// El consumidor declara el stream que lee (la misma definicion que su dueno): sin
		// el, las suscripciones a suppression esperarian a que suppression arrancara.
		if err := bus.EnsureStream("SUPPRESSION", []string{"suppression.>"}); err != nil {
			logger.Warn("ensure stream SUPPRESSION", zap.Error(err))
		}
		go outbox.RunForTenants(ctx, tenantDB, db.PoolFromCtx, bus, logger, outbox.Options{})

		worker := natsadapter.NewSuppressionWorker(bus, uc, tenantDB, logger)
		worker.Start(ctx)
		defer worker.Stop()

		// La proyeccion de interaccion lee los hitos de transactional; se declara su stream
		// con los subjects de su dueno.
		if err := bus.EnsureStream("TRANSACTIONAL", []string{"transactional.>"}); err != nil {
			logger.Warn("ensure stream TRANSACTIONAL", zap.Error(err))
		}
		engagement := natsadapter.NewEngagementWorker(bus, uc, tenantDB, logger)
		engagement.Start(ctx)
		defer engagement.Stop()
	}

	// La caducidad de una exclusion manual llega por suppression.entry.expired; el barrido
	// diario contrasta todos los contactos con suppression y corrige lo que no llego por
	// evento. No depende de NATS: consulta suppression por HTTP y publica por la outbox.
	go sweep.New(registryPool.Pool, tenantDB, uc, logger, fullSweepAt).Run(ctx)
	go prune.New(registryPool.Pool, tenantDB, uc, logger).Run(ctx)

	h := handler.NewHandler(handler.Deps{UC: uc, Perms: perms, TenantDB: tenantDB, Logger: logger, PublicBaseURL: publicBase})

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
	// Publico, sin sesion: los formularios de suscripcion. Sin el limite anterior, que frenaria
	// a los visitantes de una pagina concurrida tras la misma NAT: el envio lleva sus propios
	// cupos por IP y por formulario en Redis, y la carga del formulario el general del gateway.
	r.Mount(handler.FormsPublicPath, h.PublicFormRoutes())
	// Interno: campaigns pide la audiencia con el token interno y la empresa en la
	// cabecera. Sin limite por ip: todo llega del mismo servicio.
	r.Group(func(r chi.Router) {
		r.Use(db.TenantHeaderPoolMiddleware(tenantDB))
		r.Mount("/internal/contacts", h.InternalRoutes())
	})

	srv := server.New(port, r, logger)
	if err := srv.Run(); err != nil {
		logger.Fatal("server error", zap.Error(err))
	}
}

// formSettings son los ajustes de los formularios publicos (CONTACTS_FORM_*).
type formSettings struct {
	minFill     time.Duration
	tokenTTL    time.Duration
	perIP       int
	ipWindow    time.Duration
	perFormHour int
}

func loadFormSettings() (formSettings, error) {
	var s formSettings
	var err error
	if s.minFill, err = config.EnvDuration("CONTACTS_FORM_MIN_FILL", app.DefaultFormMinFill, time.Second, time.Minute); err != nil {
		return s, err
	}
	if s.tokenTTL, err = config.EnvDuration("CONTACTS_FORM_TOKEN_TTL", app.DefaultFormTokenTTL, 10*time.Minute, 24*time.Hour); err != nil {
		return s, err
	}
	if s.perIP, err = config.EnvInt("CONTACTS_FORM_SUBMITS_PER_IP", defaultFormSubmitsPerIP, 1, 1000); err != nil {
		return s, err
	}
	if s.ipWindow, err = config.EnvDuration("CONTACTS_FORM_IP_WINDOW", defaultFormIPWindow, time.Minute, 24*time.Hour); err != nil {
		return s, err
	}
	if s.perFormHour, err = config.EnvInt("CONTACTS_FORM_SUBMITS_PER_FORM_HOUR", defaultFormSubmitsPerFormHour, 1, 100000); err != nil {
		return s, err
	}
	return s, nil
}

// newRedis abre el Redis de la plataforma para los cupos de los formularios. Un Redis caido al
// arrancar no impide el arranque: los cupos se cuentan en la memoria de cada replica hasta que
// vuelva (pkg/middleware.NewSharedRateLimiter).
func newRedis(ctx context.Context, rc config.RedisConfig, logger *zap.Logger) (*goredis.Client, error) {
	tlsCfg, err := rc.TLSConfig()
	if err != nil {
		return nil, err
	}
	rdb := goredis.NewClient(&goredis.Options{
		Addr:                  rc.Addr(),
		Password:              rc.Password,
		TLSConfig:             tlsCfg,
		DialTimeout:           time.Second,
		ReadTimeout:           250 * time.Millisecond,
		WriteTimeout:          250 * time.Millisecond,
		PoolTimeout:           250 * time.Millisecond,
		ContextTimeoutEnabled: true,
	})
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		logger.Warn("contacts: Redis no disponible al arrancar; los cupos de los formularios se cuentan en memoria de cada replica hasta que vuelva", zap.Error(err))
	}
	return rdb, nil
}

// absoluteURL valida la URL publica del doble opt-in, obligatoria: el servicio no arranca a
// medias sin ella.
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
