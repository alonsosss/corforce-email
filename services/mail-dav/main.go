// mail-dav sirve CardDAV y CalDAV: los contactos y el calendario personales de cada buzon, sincronizables
// con iOS, Thunderbird y DAVx5 (docs/adr/0004). Plano de EMPRESA: los datos viven en el esquema mail_dav de la base de cada
// empresa. Su usuario es un buzon, no un usuario de la plataforma: cada peticion se autentica con HTTP
// Basic contra mail-auth (service "dav") y el gateway lo expone como prefijo autenticado por el servicio
// (routes.json, self_authenticated). Basic solo es admisible porque el borde termina TLS.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/alonsosss/corforce-email/pkg/config"
	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/server"
	"github.com/alonsosss/corforce-email/pkg/tenantcell"
	handler "github.com/alonsosss/corforce-email/services/mail-dav/internal/adapters/http"
	"github.com/alonsosss/corforce-email/services/mail-dav/internal/adapters/mailauth"
	natsadapter "github.com/alonsosss/corforce-email/services/mail-dav/internal/adapters/nats"
	"github.com/alonsosss/corforce-email/services/mail-dav/internal/adapters/postgres"
	"github.com/alonsosss/corforce-email/services/mail-dav/internal/adapters/tenantdb"
	"github.com/alonsosss/corforce-email/services/mail-dav/internal/app"
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
	defaultRatePerMinute      = 1200
	defaultAddressbookName    = "Contactos"
	defaultCalendarName       = "Calendario"
	defaultRealm              = "Contactos"

	defaultMaxEventBytes         = 256 << 10
	defaultMaxEventProperties    = 1000
	defaultMaxEvents             = 20000
	defaultMaxCalendars          = 10
	defaultMaxRecurrenceWork     = 20000
	defaultMaxQueryRecurrentWork = 500000

	// El limite de fila de las migraciones (mail_dav_contacts_size_check, mail_dav_events_size_check) es de
	// 4 MiB: ningun tope de tamano configurable puede pasar de ahi.
	maxObjectBytesCeiling = 4 << 20

	mailAuthTimeout = 15 * time.Second

	mailAuthCellURLsEnv = "MAIL_AUTH_CELL_URLS"
)

type settings struct {
	port         int
	basePath     string
	realm        string
	maxXMLBytes  int64
	ratePerMin   int
	app          app.Config
	mailAuth     mailauth.Config
	organization string
	internalTok  string
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
	return st, nil
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

	repo := postgres.NewRepository(&db.ContextPool{})
	uc, err := app.New(app.Deps{
		Auth:      authClient,
		Tenant:    tenantdb.NewBinder(tenantDB),
		Store:     repo,
		Calendars: repo,
		Config:    st.app,
		Logger:    logger,
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

	dav, err := handler.NewHandler(uc, handler.Config{
		BasePath: st.basePath, Realm: st.realm, MaxXMLBytes: st.maxXMLBytes, Logger: logger,
	})
	if err != nil {
		log.Fatalf("mail-dav: %v", err)
	}

	srv := server.New(st.port, router(dav, st.ratePerMin, logger), logger)
	if err := srv.Run(); err != nil {
		logger.Fatal("server error", zap.Error(err))
	}
}

// router es la cadena de la peticion. Unica entrada: el gateway; sin su token no se acepta X-Real-IP,
// que alimenta el freno de fuerza bruta de mail-auth. No usa chi: los metodos WebDAV (PROPFIND, REPORT,
// MKCOL, MKCALENDAR) no estan en su tabla de metodos.
func router(dav http.Handler, ratePerMin int, logger *zap.Logger) http.Handler {
	limiter := middleware.NewRateLimiter(ratePerMin, time.Minute)
	chain := []func(http.Handler) http.Handler{
		middleware.RequestID,
		middleware.RequireGatewayToken,
		middleware.SecureHeaders,
		middleware.Logger(logger),
		limiter.Limit,
	}
	h := dav
	for i := len(chain) - 1; i >= 0; i-- {
		h = chain[i](h)
	}
	return h
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
