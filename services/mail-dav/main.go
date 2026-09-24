// mail-dav sirve CardDAV y CalDAV: los contactos y el calendario personales de cada buzon, sincronizables
// con iOS, Thunderbird y DAVx5 (docs/adr/0004). Plano de EMPRESA: los datos viven en el esquema mail_dav de la base de cada
// empresa. Su usuario es un buzon, no un usuario de la plataforma: cada peticion se autentica con HTTP
// Basic contra mail-auth (service "dav") y el gateway lo expone como prefijo autenticado por el servicio
// (routes.json, self_authenticated). Basic solo es admisible porque el borde termina TLS. Ademas sirve al webmail
// una API JSON interna (/internal/mail-dav, que el gateway no enruta) sobre los mismos datos.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/alonsosss/corforce-email/pkg/config"
	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/pkg/mailreconcile"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/server"
	"github.com/alonsosss/corforce-email/pkg/tenantcell"
	handler "github.com/alonsosss/corforce-email/services/mail-dav/internal/adapters/http"
	"github.com/alonsosss/corforce-email/services/mail-dav/internal/adapters/mailauth"
	natsadapter "github.com/alonsosss/corforce-email/services/mail-dav/internal/adapters/nats"
	"github.com/alonsosss/corforce-email/services/mail-dav/internal/adapters/postgres"
	"github.com/alonsosss/corforce-email/services/mail-dav/internal/adapters/reconcile"
	"github.com/alonsosss/corforce-email/services/mail-dav/internal/adapters/tenantdb"
	"github.com/alonsosss/corforce-email/services/mail-dav/internal/app"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	defaultPort     = 8058
	defaultBasePath = "/api/v1/dav"

	defaultMaxVCardBytes      = 256 << 10
	defaultMaxVCardProperties = 500
	defaultMaxContacts        = 10000
	defaultMaxAddressbooks    = 10
	defaultChangesRetained    = 5000
	defaultMaxRequestBytes    = 256 << 10
	defaultMaxMailboxBytes    = 64 << 20
	defaultMaxResponseBytes   = 16 << 20
	defaultRatePerMinute      = 1200
	defaultMaxInflight        = 64
	defaultRequestTimeout     = 25 * time.Second
	defaultAuthCacheTTL       = 10 * time.Second
	defaultAuthMaxConcurrent  = 16
	defaultAddressbookName    = "Contactos"
	defaultCalendarName       = "Calendario"
	defaultRealm              = "Contactos"

	defaultMaxEventBytes         = 256 << 10
	defaultMaxEventProperties    = 1000
	defaultMaxEvents             = 20000
	defaultMaxCalendars          = 10
	defaultMaxRecurrenceWork     = 20000
	defaultMaxQueryRecurrentWork = 500000

	defaultBusyHorizonDays     = 400
	defaultBusyLookbackDays    = 7
	defaultMaxBusyPerEvent     = 1000
	defaultBusyRefreshMax      = 500
	defaultAvailabilityMax     = 20
	defaultBookingMaxDaily     = 50
	defaultBookingMaxPerVisit  = 2
	defaultBookingMaxSlots     = 300
	defaultBookingMaxWindowDay = 31

	defaultMaxImportBytes   = 4 << 20
	defaultMaxImportCards   = 1000
	defaultMaxWindowDays    = 62
	defaultMaxOccurrences   = 5000
	defaultContactsPageSize = 50
	defaultMaxPageSize      = 100

	// El limite de fila de las migraciones (mail_dav_contacts_size_check, mail_dav_events_size_check) es de
	// 4 MiB: ningun tope de tamano configurable puede pasar de ahi.
	maxObjectBytesCeiling = 4 << 20

	// Techos del espacio por buzon y de una respuesta con objetos, y de lo que se recuerda una verificacion:
	// pasado el techo dejan de acotar lo que acotan.
	maxMailboxBytesCeiling  = 4 << 30
	maxResponseBytesCeiling = 256 << 20
	maxAuthCacheTTL         = time.Minute
	authCacheEntries        = 4096
	authWait                = 5 * time.Second

	mailAuthTimeout = 15 * time.Second

	mailAuthCellURLsEnv = "MAIL_AUTH_CELL_URLS"

	// reconcileLockKey es el cerrojo de lider de la conciliacion de buzones borrados ("mdvr").
	reconcileLockKey int64 = 0x6d647672
)

type settings struct {
	port         int
	basePath     string
	realm        string
	maxXMLBytes  int64
	ratePerMin   int
	maxInflight  int
	reqTimeout   time.Duration
	authGuard    mailauth.GuardConfig
	app          app.Config
	mailAuth     mailauth.Config
	organization string
	internalTok  string
	reconcile    mailreconcile.Config
	api          handler.APILimits
}

func loadSettings(logger *zap.Logger) (settings, error) {
	var st settings
	var err error
	if st.port, err = config.EnvInt("MAIL_DAV_PORT", defaultPort, 1, config.MaxPort); err != nil {
		return st, err
	}
	st.basePath = envString("MAIL_DAV_BASE_PATH", defaultBasePath)
	st.realm = envString("MAIL_DAV_REALM", defaultRealm)
	st.app.DefaultAddressbookName = envString("MAIL_DAV_DEFAULT_ADDRESSBOOK_NAME", defaultAddressbookName)
	st.app.DefaultCalendarName = envString("MAIL_DAV_DEFAULT_CALENDAR_NAME", defaultCalendarName)

	lim := &st.app.Limits
	if lim.MaxVCardBytes, err = config.EnvInt("MAIL_DAV_MAX_VCARD_BYTES", defaultMaxVCardBytes, 1<<10, maxObjectBytesCeiling); err != nil {
		return st, err
	}
	if lim.MaxVCardProperties, err = config.EnvInt("MAIL_DAV_MAX_VCARD_PROPERTIES", defaultMaxVCardProperties, 1, 5000); err != nil {
		return st, err
	}
	if lim.MaxContactsPerMailbox, err = config.EnvInt("MAIL_DAV_MAX_CONTACTS_PER_MAILBOX", defaultMaxContacts, 1, 1_000_000); err != nil {
		return st, err
	}
	if lim.MaxAddressbooksPerMailbox, err = config.EnvInt("MAIL_DAV_MAX_ADDRESSBOOKS_PER_MAILBOX", defaultMaxAddressbooks, 1, 1000); err != nil {
		return st, err
	}
	if lim.MaxChangesRetained, err = config.EnvInt("MAIL_DAV_CHANGES_RETAINED", defaultChangesRetained, 1, 1_000_000); err != nil {
		return st, err
	}
	cal := &st.app.Calendar
	if cal.MaxEventBytes, err = config.EnvInt("MAIL_DAV_MAX_EVENT_BYTES", defaultMaxEventBytes, 1<<10, maxObjectBytesCeiling); err != nil {
		return st, err
	}
	if cal.MaxEventProperties, err = config.EnvInt("MAIL_DAV_MAX_EVENT_PROPERTIES", defaultMaxEventProperties, 1, 20000); err != nil {
		return st, err
	}
	if cal.MaxEventsPerMailbox, err = config.EnvInt("MAIL_DAV_MAX_EVENTS_PER_MAILBOX", defaultMaxEvents, 1, 1_000_000); err != nil {
		return st, err
	}
	if cal.MaxCalendarsPerMailbox, err = config.EnvInt("MAIL_DAV_MAX_CALENDARS_PER_MAILBOX", defaultMaxCalendars, 1, 1000); err != nil {
		return st, err
	}
	if cal.MaxRecurrenceWork, err = config.EnvInt("MAIL_DAV_MAX_RECURRENCE_WORK", defaultMaxRecurrenceWork, 1, 1_000_000); err != nil {
		return st, err
	}
	if cal.MaxQueryWork, err = config.EnvInt("MAIL_DAV_MAX_QUERY_RECURRENCE_WORK", defaultMaxQueryRecurrentWork, 1, 100_000_000); err != nil {
		return st, err
	}
	xmlBytes, err := config.EnvInt("MAIL_DAV_MAX_REQUEST_BYTES", defaultMaxRequestBytes, 1<<10, maxObjectBytesCeiling)
	if err != nil {
		return st, err
	}
	st.maxXMLBytes = int64(xmlBytes)
	if st.ratePerMin, err = config.EnvInt("MAIL_DAV_RATE_LIMIT_PER_MIN", defaultRatePerMinute, 1, 600000); err != nil {
		return st, err
	}
	mailboxBytes, err := config.EnvInt("MAIL_DAV_MAX_MAILBOX_BYTES", defaultMaxMailboxBytes, 1<<20, maxMailboxBytesCeiling)
	if err != nil {
		return st, err
	}
	lim.MaxMailboxBytes = int64(mailboxBytes)
	if lim.MaxReadBytes, err = config.EnvInt("MAIL_DAV_MAX_RESPONSE_BYTES", defaultMaxResponseBytes, 64<<10, maxResponseBytesCeiling); err != nil {
		return st, err
	}
	// Un objeto solo debe caber siempre en el espacio del buzon y en una respuesta.
	for name, size := range map[string]int{"MAIL_DAV_MAX_VCARD_BYTES": lim.MaxVCardBytes, "MAIL_DAV_MAX_EVENT_BYTES": cal.MaxEventBytes} {
		if size > lim.MaxReadBytes || int64(size) > lim.MaxMailboxBytes {
			return st, fmt.Errorf("%s (%d) no cabe en MAIL_DAV_MAX_RESPONSE_BYTES ni en MAIL_DAV_MAX_MAILBOX_BYTES", name, size)
		}
	}
	if st.maxInflight, err = config.EnvInt("MAIL_DAV_MAX_INFLIGHT", defaultMaxInflight, 1, 10000); err != nil {
		return st, err
	}
	if st.reqTimeout, err = config.EnvDuration("MAIL_DAV_REQUEST_TIMEOUT", defaultRequestTimeout, time.Second, 120*time.Second); err != nil {
		return st, err
	}
	if st.authGuard.CacheTTL, err = config.EnvDuration("MAIL_DAV_AUTH_CACHE_TTL", defaultAuthCacheTTL, 0, maxAuthCacheTTL); err != nil {
		return st, err
	}
	if st.authGuard.MaxConcurrent, err = config.EnvInt("MAIL_DAV_AUTH_MAX_CONCURRENT", defaultAuthMaxConcurrent, 1, 256); err != nil {
		return st, err
	}
	st.authGuard.MaxCached, st.authGuard.Wait = authCacheEntries, authWait

	if st.mailAuth, err = mailAuthFromEnv(logger); err != nil {
		return st, err
	}
	if len(st.mailAuth.CellURLs) > 0 {
		if st.organization, err = tenantcell.OrganizationURLFromEnv(); err != nil {
			return st, err
		}
	}
	if st.internalTok, err = middleware.InternalGatewayToken(); err != nil {
		return st, err
	}
	if st.reconcile, err = mailreconcile.ConfigFromEnv("MAIL_DAV"); err != nil {
		return st, err
	}
	if st.api, err = apiLimitsFromEnv(st.maxXMLBytes); err != nil {
		return st, err
	}
	st.api.MaxITIPBodyBytes = 2*int64(cal.MaxEventBytes) + multipartSlack
	if st.app.Scheduling, err = schedulingFromEnv(); err != nil {
		return st, err
	}
	return st, nil
}

// multipartSlack es lo que se admite de mas sobre dos veces el tope de un evento en el cuerpo JSON de una
// invitacion: el iCalendar escapado en JSON y las direcciones del buzon.
const multipartSlack = 64 << 10

// schedulingFromEnv lee la planificacion (disponibilidad, invitaciones y citas). MAIL_DAV_BUSY_HORIZON_DAYS=0 la
// apaga: los eventos se guardan sin ocupacion materializada y esas rutas responden 503.
func schedulingFromEnv() (app.SchedulingConfig, error) {
	var c app.SchedulingConfig
	days := func(key string, def, lo, hi int) (time.Duration, error) {
		n, err := config.EnvInt(key, def, lo, hi)
		return time.Duration(n) * 24 * time.Hour, err
	}
	var err error
	if c.BusyHorizon, err = days("MAIL_DAV_BUSY_HORIZON_DAYS", defaultBusyHorizonDays, 0, 3650); err != nil {
		return c, err
	}
	if c.BusyLookback, err = days("MAIL_DAV_BUSY_LOOKBACK_DAYS", defaultBusyLookbackDays, 1, 62); err != nil {
		return c, err
	}
	if c.BookingMaxWindow, err = days("MAIL_DAV_BOOKING_MAX_WINDOW_DAYS", defaultBookingMaxWindowDay, 1, 62); err != nil {
		return c, err
	}
	for _, v := range []struct {
		key         string
		dst         *int
		def, lo, hi int
	}{
		{"MAIL_DAV_MAX_BUSY_PER_EVENT", &c.MaxBusyPerEvent, defaultMaxBusyPerEvent, 1, 100_000},
		{"MAIL_DAV_BUSY_REFRESH_MAX", &c.BusyRefreshMax, defaultBusyRefreshMax, 1, 10_000},
		{"MAIL_DAV_AVAILABILITY_MAX_ADDRESSES", &c.MaxAvailabilityAddresses, defaultAvailabilityMax, 1, 50},
		{"MAIL_DAV_BOOKING_MAX_DAILY", &c.BookingMaxDaily, defaultBookingMaxDaily, 1, 1000},
		{"MAIL_DAV_BOOKING_MAX_PER_VISITOR", &c.BookingMaxPerVisitor, defaultBookingMaxPerVisit, 1, 100},
		{"MAIL_DAV_BOOKING_MAX_SLOTS", &c.BookingMaxSlots, defaultBookingMaxSlots, 1, 5000},
	} {
		if *v.dst, err = config.EnvInt(v.key, v.def, v.lo, v.hi); err != nil {
			return c, err
		}
	}
	return c, c.Validate()
}

// apiLimitsFromEnv lee los topes de la API interna del webmail. El cuerpo JSON de un alta se acota como el de
// una peticion DAV (MAIL_DAV_MAX_REQUEST_BYTES).
func apiLimitsFromEnv(maxBody int64) (handler.APILimits, error) {
	lim := handler.APILimits{MaxBodyBytes: maxBody}
	importBytes, err := config.EnvInt("MAIL_DAV_MAX_IMPORT_BYTES", defaultMaxImportBytes, 1<<10, 64<<20)
	if err != nil {
		return lim, err
	}
	lim.MaxImportBytes = int64(importBytes)
	if lim.MaxImportCards, err = config.EnvInt("MAIL_DAV_MAX_IMPORT_CARDS", defaultMaxImportCards, 1, 100_000); err != nil {
		return lim, err
	}
	if lim.MaxWindowDays, err = config.EnvInt("MAIL_DAV_MAX_EVENT_WINDOW_DAYS", defaultMaxWindowDays, 1, 366); err != nil {
		return lim, err
	}
	if lim.MaxOccurrences, err = config.EnvInt("MAIL_DAV_MAX_OCCURRENCES", defaultMaxOccurrences, 1, 100_000); err != nil {
		return lim, err
	}
	if lim.MaxPerPage, err = config.EnvInt("MAIL_DAV_MAX_CONTACTS_PAGE_SIZE", defaultMaxPageSize, 1, 1000); err != nil {
		return lim, err
	}
	if lim.DefaultPerPage, err = config.EnvInt("MAIL_DAV_CONTACTS_PAGE_SIZE", defaultContactsPageSize, 1, lim.MaxPerPage); err != nil {
		return lim, err
	}
	return lim, lim.Validate()
}

// mailAuthFromEnv arma el destino de la verificacion. MAIL_AUTH_URL es el mail-auth de la celda base
// (el mismo que usa el webmail) y MAIL_AUTH_CELL_URLS, opcional, los de las demas celdas
// ("celda=https://host:puerto" separadas por comas).
func mailAuthFromEnv(logger *zap.Logger) (mailauth.Config, error) {
	cfg := mailauth.Config{Timeout: mailAuthTimeout}
	cfg.BaseURL = strings.TrimSpace(os.Getenv("MAIL_AUTH_URL"))
	if cfg.BaseURL == "" {
		return cfg, errors.New("MAIL_AUTH_URL es obligatorio: mail-dav no valida credenciales, las verifica mail-auth")
	}
	cfg.BaseCell = strings.TrimSpace(os.Getenv(tenantcell.BaseCellEnv))
	if raw := strings.TrimSpace(os.Getenv(mailAuthCellURLsEnv)); raw != "" {
		cfg.CellURLs = map[string]string{}
		for _, entry := range strings.Split(raw, ",") {
			code, target, ok := strings.Cut(strings.TrimSpace(entry), "=")
			code, target = strings.TrimSpace(code), strings.TrimSpace(target)
			if !ok || !tenantcell.ValidCode(code) || target == "" {
				return cfg, fmt.Errorf("%s: entrada %q: se espera celda=https://host:puerto", mailAuthCellURLsEnv, entry)
			}
			if _, dup := cfg.CellURLs[code]; dup {
				return cfg, fmt.Errorf("%s: celda %q repetida", mailAuthCellURLsEnv, code)
			}
			cfg.CellURLs[code] = target
		}
		if cfg.BaseCell == "" {
			return cfg, fmt.Errorf("%s es obligatorio cuando %s declara celdas", tenantcell.BaseCellEnv, mailAuthCellURLsEnv)
		}
	}
	insecure, err := envBool("MAIL_DAV_TLS_INSECURE_SKIP_VERIFY")
	if err != nil {
		return cfg, err
	}
	if insecure && !config.DeclaredDevelopmentOrTest() {
		return cfg, errors.New("MAIL_DAV_TLS_INSECURE_SKIP_VERIFY solo vale con ENVIRONMENT development o test: por esa conexion viaja la contrasena del buzon")
	}
	if cfg.TLS, err = config.ClientTLS("", os.Getenv("MAIL_DAV_TLS_CA_FILE")); err != nil {
		return cfg, fmt.Errorf("TLS de mail-auth: %w", err)
	}
	if insecure {
		logger.Warn("mail-dav: MAIL_DAV_TLS_INSECURE_SKIP_VERIFY=true, no se verifica el certificado de mail-auth (solo desarrollo)")
		cfg.TLS.InsecureSkipVerify = true
	}
	return cfg, nil
}

func main() {
	logger, _ := zap.NewProduction()
	defer logger.Sync()
	response.SetUnexpectedLogger(logger)

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	st, err := loadSettings(logger)
	if err != nil {
		log.Fatalf("mail-dav: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	registryPool, err := db.NewPool(ctx, cfg.Postgres.DSN(), logger)
	if err != nil {
		log.Fatalf("connect db: %v", err)
	}
	defer registryPool.Close()
	tenantDB, mgr := db.NewTenantRouting(registryPool.Pool, cfg.Postgres, logger)
	defer mgr.CloseAll()

	var resolver mailauth.CellResolver
	if len(st.mailAuth.CellURLs) > 0 {
		resolver = tenantcell.NewDomainResolver(st.organization, st.internalTok, logger)
	}
	authClient, err := mailauth.New(st.mailAuth, resolver)
	if err != nil {
		log.Fatalf("mail-dav: %v", err)
	}

	guard, err := mailauth.NewGuard(authClient, st.authGuard)
	if err != nil {
		log.Fatalf("mail-dav: %v", err)
	}
	repo := postgres.NewRepository(&db.ContextPool{})
	uc, err := app.New(app.Deps{
		Auth:       guard,
		Tenant:     tenantdb.NewBinder(tenantDB),
		Store:      repo,
		Calendars:  repo,
		Index:      repo,
		Scheduling: repo,
		Config:     st.app,
		Logger:     logger,
	})
	if err != nil {
		log.Fatalf("mail-dav: %v", err)
	}
	// Sin NATS el servicio sirve igual: los contactos y eventos de un buzon borrado siguen en la base de su
	// empresa hasta que haya bus, porque el stream conserva el evento y el durable lo recoge al suscribirse.
	if bus, err := events.NewBus(cfg.NATS.URL, logger); err != nil {
		logger.Warn("mail-dav: NATS no disponible; sin retirada de los datos de buzones borrados", zap.Error(err))
	} else {
		defer bus.Close()
		go natsadapter.NewConsumer(bus, uc, logger).Run(ctx)
	}

	// La conciliacion retira lo que el consumidor no vio: buzones borrados antes de que existiera o mas alla de
	// lo que el stream conserva, y filas que una peticion en vuelo escribio despues del evento.
	if st.reconcile.Enabled() {
		directory, err := mailreconcile.NewDirectoryFromEnv(logger)
		if err != nil {
			log.Fatalf("mail-dav: conciliacion de buzones borrados: %v", err)
		}
		sweeper := mailreconcile.New(mailreconcile.Deps{
			Name: "mail-dav", Config: st.reconcile,
			Lock: func(c context.Context) (func(), bool) {
				return db.TryLeaderLock(c, registryPool.Pool, reconcileLockKey)
			},
			Tenants:   mailreconcile.TenantsOf(tenantDB),
			Directory: directory,
			Store:     reconcile.NewStore(uc),
			Logger:    logger,
		})
		go sweeper.Run(ctx)
	} else {
		logger.Info("mail-dav: conciliacion de buzones borrados desactivada (MAIL_DAV_RECONCILE_INTERVAL=0)")
	}

	dav, err := handler.NewHandler(uc, handler.Config{
		BasePath: st.basePath, Realm: st.realm, MaxXMLBytes: st.maxXMLBytes, Logger: logger,
	})
	if err != nil {
		log.Fatalf("mail-dav: %v", err)
	}

	api, err := handler.NewAPI(uc, st.api, logger)
	if err != nil {
		log.Fatalf("mail-dav: %v", err)
	}

	srv := server.New(st.port, router(dav, api.Routes(), st, logger), logger)
	runErr := srv.Run()
	// Los cierres de abajo (NATS, pools) van antes que el cancel diferido: se detiene primero el consumidor
	// para que no pida conexiones a un pool que se esta cerrando.
	cancel()
	if runErr != nil {
		logger.Fatal("server error", zap.Error(runErr))
	}
}

// router es la cadena de la peticion. Unica entrada: el gateway; sin su token no se acepta X-Real-IP,
// que alimenta el freno de fuerza bruta de mail-auth. No usa chi: los metodos WebDAV (PROPFIND, REPORT,
// MKCOL, MKCALENDAR) no estan en su tabla de metodos. Tras el cupo, las peticiones que se atienden a la vez
// (DAV y API interna juntas) y el tiempo de cada una estan acotados: el pool de la base de una empresa es de
// diez conexiones y una peticion que espera una o que se queda calculando no puede sostenerse indefinidamente.
//
// InternalPrefix va a la API interna del webmail: solo servicios de la plataforma (RequireInternalCaller) y
// con el cupo por buzon, porque todas sus peticiones llegan desde las pocas IP del webmail.
func router(dav, api http.Handler, st settings, logger *zap.Logger) http.Handler {
	slots := make(chan struct{}, st.maxInflight)
	davChain := chain(dav,
		middleware.NewRateLimiter(st.ratePerMin, time.Minute).Limit,
		limitInflight(slots, func(w http.ResponseWriter) { http.Error(w, "servicio ocupado", http.StatusServiceUnavailable) }),
		withDeadline(st.reqTimeout),
	)
	apiChain := chain(api,
		middleware.InjectFromGateway,
		middleware.RequireInternalCaller,
		limitPerMailbox(middleware.NewRateLimiter(st.ratePerMin, time.Minute)),
		limitInflight(slots, func(w http.ResponseWriter) {
			response.Err(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "servicio ocupado")
		}),
		withDeadline(st.reqTimeout),
	)
	split := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == handler.InternalPrefix || strings.HasPrefix(r.URL.Path, handler.InternalPrefix+"/") {
			apiChain.ServeHTTP(w, r)
			return
		}
		davChain.ServeHTTP(w, r)
	})
	return chain(split, middleware.RequestID, middleware.RequireGatewayToken, middleware.SecureHeaders, middleware.Logger(logger))
}

// chain envuelve h con los middlewares en orden: el primero es el mas externo.
func chain(h http.Handler, mws ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// limitInflight rechaza con 503 lo que llega cuando ya se atienden tantas peticiones como huecos tiene slots:
// mejor que el cliente reintente que acumular peticiones esperando una conexion de la base.
func limitInflight(slots chan struct{}, busy func(http.ResponseWriter)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
				next.ServeHTTP(w, r)
			default:
				w.Header().Set("Retry-After", "5")
				busy(w)
			}
		})
	}
}

// limitPerMailbox es el cupo de la API interna por buzon (la cabecera que lo nombra): una cabecera que no es
// un UUID cuenta en un cupo comun y la rechaza despues el manejador.
func limitPerMailbox(rl *middleware.RateLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := "mailbox:invalid"
			if id, err := uuid.Parse(strings.TrimSpace(r.Header.Get(handler.MailboxHeader))); err == nil {
				key = "mailbox:" + id.String()
			} else if tenant, ok := publicBookingTenant(r.URL.Path); ok {
				key = "booking:" + tenant
			}
			if ok, reset := rl.AllowKey(r.Context(), key); !ok {
				w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(reset.Seconds())))))
				response.Err(w, http.StatusTooManyRequests, "RATE_LIMITED", "demasiadas peticiones del buzon")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// publicBookingTenant es la empresa de una ruta de la pagina publica de citas, que no lleva buzon: su cupo es por
// empresa, para que el trafico anonimo de una no gaste el de las demas.
func publicBookingTenant(path string) (string, bool) {
	rest, ok := strings.CutPrefix(path, handler.PublicBookingPrefix+"/")
	if !ok {
		return "", false
	}
	tenant, _, _ := strings.Cut(rest, "/")
	id, err := uuid.Parse(tenant)
	if err != nil {
		return "", false
	}
	return id.String(), true
}

// withDeadline pone un plazo a la peticion: al vencer se cancelan sus consultas a la base y el trabajo que
// hace el servicio, en vez de seguir despues de que el servidor ya cerro la respuesta.
func withDeadline(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func envString(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envBool(key string) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return false, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s=%q no es true ni false", key, raw)
	}
	return v, nil
}
