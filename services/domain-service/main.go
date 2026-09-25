package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/pkg/auth"
	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/config"
	"github.com/alonsosss/corforce-email/pkg/crypto"
	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/pkg/keyrotation"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/outbox"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/server"
	"github.com/alonsosss/corforce-email/pkg/tenantcell"
	"github.com/alonsosss/corforce-email/services/domain-service/internal/adapters/cloudflare"
	dnsadapter "github.com/alonsosss/corforce-email/services/domain-service/internal/adapters/dns"
	handler "github.com/alonsosss/corforce-email/services/domain-service/internal/adapters/http"
	"github.com/alonsosss/corforce-email/services/domain-service/internal/adapters/maildirectorycli"
	"github.com/alonsosss/corforce-email/services/domain-service/internal/adapters/mailsecuritycli"
	natsadapter "github.com/alonsosss/corforce-email/services/domain-service/internal/adapters/nats"
	"github.com/alonsosss/corforce-email/services/domain-service/internal/adapters/organizationcli"
	outboxadapter "github.com/alonsosss/corforce-email/services/domain-service/internal/adapters/outbox"
	"github.com/alonsosss/corforce-email/services/domain-service/internal/adapters/postgres"
	"github.com/alonsosss/corforce-email/services/domain-service/internal/adapters/sescli"
	"github.com/alonsosss/corforce-email/services/domain-service/internal/app"
	"github.com/alonsosss/corforce-email/services/domain-service/internal/domain"
	"github.com/alonsosss/corforce-email/services/domain-service/internal/ports"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	defaultPort            = 8043
	defaultRecheckInterval = 6 * time.Hour
	defaultSweepTimeout    = 5 * time.Minute
	defaultSweepWorkers    = 4

	// Cada barrido consulta el DNS de todos los dominios de todas las empresas y llama a sus
	// celdas; el suelo deja caber el tiempo por empresa por defecto. El techo es el de la alerta
	// BarridoDeDominiosSinCelda (ops/observability/prometheus/rules/plataforma.yml), que avisa con
	// fallos en dos barridos seguidos dentro de su ventana de 6h30m: con un intervalo mayor no
	// avisaria nunca. Alargarlo exige alargar a la vez la ventana y la espera de la alerta.
	minRecheckInterval = defaultSweepTimeout
	maxRecheckInterval = 6 * time.Hour
	// Verificar un dominio son hasta seis consultas DNS de 5 s y sus llamadas a la celda: con
	// menos de un minuto una empresa lenta no terminaria ni el primero.
	minSweepTimeout = time.Minute
	// Cada empresa en vuelo ocupa conexiones de su pool (hasta 10, pkg/db) a traves de
	// pgbouncer, cuyo max_client_conn (4000) comparten todos los servicios.
	maxSweepWorkers = 64
	// La clave anterior de una rotacion programada sigue en los motores y en la zona del cliente
	// durante la gracia, contada desde la ultima vez que pudo firmar. Suelo: lo que un mensaje
	// firmado con ella puede seguir en la cola de Postfix (maximal_queue_lifetime = 5d en
	// deploy/mail/postfix/conf/main.cf.base) mas un dia, el TTL mas largo habitual de un TXT; con menos,
	// correo aun en cola llegaria al receptor cuando su TXT ya no esta. Alargar la cola exige subir
	// este suelo; ops/scaffold/check-dkim-grace.sh lo comprueba. Techo: treinta dias; una clave
	// comprometida no espera a la gracia, se revoca (RevokeDKIM).
	minDKIMRotationGrace = 144 * time.Hour
	maxDKIMRotationGrace = 720 * time.Hour
	// El historial de comprobaciones crece una fila por registro en cada verificacion.
	maxCheckRetention = 365 * 24 * time.Hour

	// sweepLockKey serializa el barrido entre replicas (advisory lock en el registro).
	sweepLockKey int64 = 0x646f6d73 // "doms"

	// Instancias por celda de los servicios de celda: las mismas variables que lee el gateway
	// (Modelo_de_Datos_y_Celdas.md, 5.4), con MAIL_DIRECTORY_URL y MAIL_SECURITY_URL como
	// destinos base de la celda tenantcell.BaseCellEnv.
	mailDirectoryCellHostsEnv = "MAIL_DIRECTORY_CELL_HOSTS"
	mailSecurityCellHostsEnv  = "MAIL_SECURITY_CELL_HOSTS"

	// cloudflareAPIURLEnv sobrescribe la API de Cloudflare solo para pruebas (un Cloudflare falso).
	// Fuera de desarrollo y pruebas tiene que ser https: por ahi viaja el token de la empresa.
	cloudflareAPIURLEnv = "CLOUDFLARE_API_URL"
	stepUpModeEnv       = "STEP_UP_MODE"
)

// settings es la configuracion propia del servicio. Los valores que aparecen en la zona
// DNS del cliente y las URLs de los servicios vecinos no tienen defecto: un valor
// inventado enrutaria correo real a un sitio equivocado.
type settings struct {
	platformHostname string
	platform         domain.PlatformDNS
	mailDirectoryURL string
	mailSecurityURL  string
	// organizationURL sirve el indice global de dominios y, con varias celdas, la celda de cada
	// empresa.
	organizationURL string
	// directoryTargets y securityTargets eligen la instancia de la celda de cada empresa.
	directoryTargets *tenantcell.Targets
	securityTargets  *tenantcell.Targets
	internalToken    string
	perms            *authz.Checker
	// cloudflareURL es la API a la que domain-service lleva el token de Cloudflare de cada empresa.
	cloudflareURL string
	// stepUp exige reconfirmar la identidad para conectar un proveedor DNS (STEP_UP_MODE).
	stepUp          func(http.Handler) http.Handler
	dnsResolver     string
	rotationGrace   time.Duration
	recheckInterval time.Duration
	sweepWorkers    int
	sweepTimeout    time.Duration
	checkRetention  time.Duration
	port            int
}

// loadSettings lee y valida la configuracion. Siempre necesita ORGANIZATION_URL: cada dominio se
// reclama en el indice global de organization antes de activarlo. Con varias celdas
// (tenantcell.BaseCellEnv) organization da ademas la celda de cada empresa; con una, las llamadas
// a los servicios de celda van al destino base sin preguntarla.
func loadSettings(logger *zap.Logger) (settings, error) {
	var missing []string
	require := func(key string) string {
		v := strings.TrimSpace(os.Getenv(key))
		if v == "" {
			missing = append(missing, key)
		}
		return v
	}
	s := settings{
		platformHostname: require("MAIL_HOSTNAME"),
		platform: domain.PlatformDNS{
			MXHostname: require("MAIL_MX_HOSTNAME"),
			SPFInclude: require("MAIL_SPF_INCLUDE"),
			DMARCRUA:   require("MAIL_DMARC_RUA"),
		},
		dnsResolver: strings.TrimSpace(os.Getenv("MAIL_DNS_RESOLVER")),
	}
	s.platform.TLSRPTRUA = strings.TrimSpace(os.Getenv("MAIL_TLSRPT_RUA"))
	if len(missing) > 0 {
		return s, fmt.Errorf("faltan variables de entorno obligatorias: %s", strings.Join(missing, ", "))
	}
	if err := domain.ValidatePlatformHostname(s.platformHostname); err != nil {
		return s, fmt.Errorf("MAIL_HOSTNAME %q: %w", s.platformHostname, err)
	}
	if s.platform.TLSRPTRUA != "" {
		if err := domain.ValidateReportAddress(s.platform.TLSRPTRUA); err != nil {
			return s, fmt.Errorf("MAIL_TLSRPT_RUA %q: %w", s.platform.TLSRPTRUA, err)
		}
	}
	if !strings.HasPrefix(s.platform.SPFInclude, "include:") {
		return s, fmt.Errorf("MAIL_SPF_INCLUDE debe ser un mecanismo include: (p. ej. include:spf.%s)", s.platformHostname)
	}
	var err error
	if s.port, err = config.EnvInt("DOMAIN_SERVICE_PORT", defaultPort, 1, config.MaxPort); err != nil {
		return s, err
	}
	if s.recheckInterval, err = config.EnvDuration("DOMAIN_RECHECK_INTERVAL", defaultRecheckInterval, minRecheckInterval, maxRecheckInterval); err != nil {
		return s, err
	}
	// Una empresa no retiene el barrido mas alla de su intervalo.
	if s.sweepTimeout, err = config.EnvDuration("DOMAIN_SWEEP_TENANT_TIMEOUT", defaultSweepTimeout, minSweepTimeout, s.recheckInterval); err != nil {
		return s, err
	}
	if s.sweepWorkers, err = config.EnvInt("DOMAIN_SWEEP_CONCURRENCY", defaultSweepWorkers, 1, maxSweepWorkers); err != nil {
		return s, err
	}
	if s.rotationGrace, err = config.EnvDuration("MAIL_DKIM_ROTATION_GRACE", app.DefaultDKIMRotationGrace, minDKIMRotationGrace, maxDKIMRotationGrace); err != nil {
		return s, err
	}
	// Un dominio pendiente se reverifica durante su ventana: su historial no se poda antes.
	if s.checkRetention, err = config.EnvDuration("DOMAIN_CHECK_RETENTION", app.DefaultCheckRetention, app.DefaultPendingRecheckWindow, maxCheckRetention); err != nil {
		return s, err
	}
	if s.mailDirectoryURL, err = config.RequiredServiceURL("MAIL_DIRECTORY_URL"); err != nil {
		return s, err
	}
	if s.mailSecurityURL, err = config.RequiredServiceURL("MAIL_SECURITY_URL"); err != nil {
		return s, err
	}
	if s.perms, err = authz.CheckerFromEnv(); err != nil {
		return s, err
	}
	if s.cloudflareURL, err = cloudflareURLFromEnv(); err != nil {
		return s, err
	}
	if s.stepUp, err = stepUpFromEnv(); err != nil {
		return s, err
	}
	token, err := middleware.InternalGatewayToken()
	if err != nil {
		return s, err
	}
	s.internalToken = token
	if s.organizationURL, err = tenantcell.OrganizationURLFromEnv(); err != nil {
		return s, fmt.Errorf("domain-service reclama cada dominio en el indice de organization antes de activarlo: %w", err)
	}

	cells, err := tenantcell.LoadInstances(os.Getenv, tenantcell.BaseCellEnv, mailDirectoryCellHostsEnv, mailSecurityCellHostsEnv)
	if err != nil {
		return s, err
	}
	var resolver *tenantcell.Resolver
	if cells.BaseCell != "" {
		resolver = tenantcell.NewResolver(s.organizationURL, token, logger)
	}
	if s.directoryTargets, err = cells.Targets(mailDirectoryCellHostsEnv, s.mailDirectoryURL, resolver); err != nil {
		return s, err
	}
	if s.securityTargets, err = cells.Targets(mailSecurityCellHostsEnv, s.mailSecurityURL, resolver); err != nil {
		return s, err
	}
	return s, nil
}

// cloudflareURLFromEnv lee la URL base de la API de Cloudflare con la regla comun de URLs internas
// (sin ruta, credenciales, consulta ni fragmento). http solo se admite con ENVIRONMENT de desarrollo
// o pruebas.
func cloudflareURLFromEnv() (string, error) {
	u, err := config.ServiceURL(cloudflareAPIURLEnv, cloudflare.DefaultBaseURL)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(u, "https://") && !config.DeclaredDevelopmentOrTest() {
		return "", fmt.Errorf("%s=%q: fuera de desarrollo y pruebas la API de Cloudflare va por https", cloudflareAPIURLEnv, u)
	}
	return u, nil
}

// stepUpFromEnv arma el step-up de conectar un proveedor DNS, que guarda una credencial de terceros.
// Con STEP_UP_MODE=enforce verifica el token de step-up con las claves publicas de identity
// (JWT_PUBLIC_KEYS) y sin ellas no arranca: aceptaria cualquier peticion o ninguna. Sin enforce,
// middleware.RequireStepUp deja pasar, como en identity.
func stepUpFromEnv() (func(http.Handler) http.Handler, error) {
	if !strings.EqualFold(os.Getenv(stepUpModeEnv), "enforce") {
		return middleware.RequireStepUp(nil), nil
	}
	keys, err := auth.KeySetFromEnv()
	if err != nil {
		return nil, fmt.Errorf("%s=enforce: domain-service verifica el step-up de conectar un proveedor DNS: %w", stepUpModeEnv, err)
	}
	verifier, err := auth.NewVerifier(keys)
	if err != nil {
		return nil, fmt.Errorf("%s=enforce: %w", stepUpModeEnv, err)
	}
	return middleware.RequireStepUp(verifier), nil
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
		log.Fatalf("domain-service: %v", err)
	}
	keyRing, err := crypto.LoadKeyRing("MAIL_ENCRYPTION_KEY", "MAIL_ENCRYPTION_KEYS_OLD")
	if err != nil {
		log.Fatalf("domain-service: cifrado de claves DKIM y tokens de proveedores DNS: %v", err)
	}
	if base := st.directoryTargets.BaseCell(); base != "" {
		logger.Info("servicios de celda por la celda de cada empresa", zap.String("base_cell", base),
			zap.Strings("mail_directory_cells", st.directoryTargets.Cells()),
			zap.Strings("mail_security_cells", st.securityTargets.Cells()))
	} else {
		logger.Info("una celda: mail-directory y mail-security en su destino base, sin resolver la celda de cada empresa")
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

	publisher := natsadapter.NewPublisher(nil)
	if bus, err := events.NewBus(cfg.NATS.URL, logger); err != nil {
		// Los eventos de las claves DKIM esperan en la outbox de cada empresa hasta que un rele
		// los entregue.
		logger.Warn("domain-service: NATS no disponible, sin eventos ni rele de outbox", zap.Error(err))
	} else {
		defer bus.Close()
		if err := bus.EnsureStream("DOMAINS", []string{"domains.>"}); err != nil {
			logger.Warn("ensure stream DOMAINS", zap.Error(err))
		}
		publisher = natsadapter.NewPublisher(bus)
		go outbox.RunForTenants(ctx, tenantDB, db.PoolFromCtx, bus, logger, outbox.Options{})
	}

	sesOptions := sescli.OptionsFromEnv()
	var sesIdentities ports.SESIdentityClient
	var sendingEvents ports.SendingEvents
	if sesOptions.Enabled() {
		client, err := sescli.New(ctx, sesOptions)
		if err != nil {
			log.Fatalf("domain-service: identidades de Amazon SES: %v", err)
		}
		sesIdentities = client
		st.platform.SESRegion = sesOptions.Region
		logger.Info("identidades de Amazon SES activas: los dominios de envio se dan de alta en SES al verificarse",
			zap.String("region", sesOptions.Region), zap.String("configuration_set", sesOptions.ConfigurationSet))
	} else {
		logger.Warn("identidades de Amazon SES desactivadas (sin SES_IDENTITIES_ACCESS_KEY_ID ni SES_IDENTITIES_SECRET_ACCESS_KEY): " +
			"los dominios de envio no se dan de alta en SES ni se piden sus registros MAIL FROM")
	}

	repo := postgres.NewRepository(ctxPool)
	directory := maildirectorycli.New(tenantcell.NewCaller("mail-directory", st.directoryTargets, st.internalToken, logger, tenantcell.CallerOptions{}))
	keyEvents := outboxadapter.NewPublisher(ctxPool)
	if sesIdentities != nil {
		sendingEvents = keyEvents
	}
	uc := app.New(app.Deps{
		Repo:                 repo,
		DNS:                  dnsadapter.New(st.dnsResolver),
		Cipher:               keyRing,
		MailDirectory:        directory,
		MTASTS:               directory,
		MailSecurity:         mailsecuritycli.New(tenantcell.NewCaller("mail-security", st.securityTargets, st.internalToken, logger, tenantcell.CallerOptions{})),
		DomainIndex:          organizationcli.New(st.organizationURL, st.internalToken),
		Events:               publisher,
		KeyEvents:            keyEvents,
		DNSProviders:         repo,
		DNSAPIs:              map[domain.DNSProvider]ports.DNSProviderAPI{domain.DNSProviderCloudflare: cloudflare.New(st.cloudflareURL, cloudflare.DefaultTimeout)},
		DNSEvents:            keyEvents,
		SES:                  sesIdentities,
		SendingEvents:        sendingEvents,
		SESConfigurationSet:  sesOptions.ConfigurationSet,
		Platform:             st.platform,
		PlatformHostname:     st.platformHostname,
		DKIMRotationGrace:    st.rotationGrace,
		PendingRecheckWindow: app.DefaultPendingRecheckWindow,
		CheckRetention:       st.checkRetention,
		Logger:               logger,
	})

	// Barrido de fondo: reverifica dominios por empresa en paralelo, con timeout por
	// empresa, y solo en una replica a la vez (leader lock sobre el registro). Una
	// pasada al arrancar cubre lo que quedo pendiente mientras el servicio no corria.
	go runSweeps(ctx, tenantDB, registryPool, uc, st, logger)
	// Con llaves en MAIL_ENCRYPTION_KEYS_OLD, re-cifra bajo la activa las claves DKIM y los tokens de
	// proveedores DNS de todas las empresas, al arrancar y cada hora.
	sealed := postgres.SealedColumns()
	go keyrotation.Run(ctx, keyRing, "domains (bases de empresa)", func(c context.Context) (keyrotation.Result, error) {
		return keyrotation.TenantPass(c, keyRing, tenantDB, st.sweepWorkers, st.sweepTimeout, logger, sealed...)
	}, keyrotation.NewMetrics("domain_service", "Claves DKIM privadas y tokens de proveedores DNS"), logger)

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RequireGatewayToken)
	r.Use(middleware.InjectFromGateway)
	r.Use(db.TenantPoolMiddleware(tenantDB))
	r.Use(middleware.SecureHeaders)
	r.Use(middleware.Logger(logger))
	r.Use(middleware.NewRateLimiter(120, time.Minute).Limit)
	r.Mount("/", handler.NewHandler(uc, st.perms, st.stepUp).Routes())

	srv := server.New(st.port, r, logger)
	if err := srv.Run(); err != nil {
		logger.Fatal("server error", zap.Error(err))
	}
}

func runSweeps(ctx context.Context, tenantDB *db.TenantDB, registry *db.Pool, uc *app.UseCase, st settings, logger *zap.Logger) {
	sweep := func() {
		release, ok := db.TryLeaderLock(ctx, registry.Pool, sweepLockKey)
		if !ok {
			logger.Info("barrido de dominios: otra replica lo esta ejecutando")
			return
		}
		defer release()
		started := time.Now()
		// Las empresas se barren en paralelo: el acumulado se protege con un mutex.
		var mu sync.Mutex
		var total app.SweepReport
		err := tenantDB.ForEachActiveTenantConcurrent(ctx, st.sweepWorkers, st.sweepTimeout, func(tctx context.Context, tenantID string) {
			id, err := uuid.Parse(tenantID)
			if err != nil {
				return
			}
			rep := uc.SweepTenant(tctx, id)
			mu.Lock()
			total.Rechecked += rep.Rechecked
			total.Failed += rep.Failed
			total.Deactivated += rep.Deactivated
			total.Retired += rep.Retired
			total.Revoked += rep.Revoked
			total.SESRetried += rep.SESRetried
			total.Pruned += rep.Pruned
			mu.Unlock()
		})
		if err != nil {
			logger.Error("barrido de dominios: listar empresas", zap.Error(err))
			return
		}
		logger.Info("barrido de dominios completado",
			zap.Int("rechecked", total.Rechecked), zap.Int("failed", total.Failed),
			zap.Int("deactivated", total.Deactivated),
			zap.Int("dkim_retired", total.Retired), zap.Int("dkim_revocations_completed", total.Revoked),
			zap.Int("ses_retried", total.SESRetried),
			zap.Int64("checks_pruned", total.Pruned),
			zap.Duration("took", time.Since(started)))
	}

	sweep()
	ticker := time.NewTicker(st.recheckInterval)
	defer ticker.Stop()
	for range ticker.C {
		sweep()
	}
}
