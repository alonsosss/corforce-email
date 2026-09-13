package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/config"
	"github.com/alonsosss/corforce-email/pkg/crypto"
	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/server"
	dnsadapter "github.com/alonsosss/corforce-email/services/domain-service/internal/adapters/dns"
	handler "github.com/alonsosss/corforce-email/services/domain-service/internal/adapters/http"
	"github.com/alonsosss/corforce-email/services/domain-service/internal/adapters/maildirectorycli"
	"github.com/alonsosss/corforce-email/services/domain-service/internal/adapters/mailsecuritycli"
	natsadapter "github.com/alonsosss/corforce-email/services/domain-service/internal/adapters/nats"
	"github.com/alonsosss/corforce-email/services/domain-service/internal/adapters/postgres"
	"github.com/alonsosss/corforce-email/services/domain-service/internal/app"
	"github.com/alonsosss/corforce-email/services/domain-service/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	defaultPort            = 8043
	defaultRecheckInterval = 6 * time.Hour
	defaultSweepTimeout    = 5 * time.Minute
	defaultSweepWorkers    = 4
	// sweepLockKey serializa el barrido entre replicas (advisory lock en el registro).
	sweepLockKey int64 = 0x646f6d73 // "doms"
)

// settings es la configuracion propia del servicio. Los valores que aparecen en la zona
// DNS del cliente y las URLs de los servicios vecinos no tienen defecto: un valor
// inventado enrutaria correo real a un sitio equivocado.
type settings struct {
	platformHostname string
	platform         domain.PlatformDNS
	mailDirectoryURL string
	mailSecurityURL  string
	internalToken    string
	dnsResolver      string
	rotationGrace    time.Duration
	recheckInterval  time.Duration
	sweepWorkers     int
	sweepTimeout     time.Duration
	checkRetention   time.Duration
	port             int
}

func loadSettings() (settings, error) {
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
		mailDirectoryURL: require("MAIL_DIRECTORY_URL"),
		mailSecurityURL:  require("MAIL_SECURITY_URL"),
		dnsResolver:      strings.TrimSpace(os.Getenv("MAIL_DNS_RESOLVER")),
		rotationGrace:    envDuration("MAIL_DKIM_ROTATION_GRACE", app.DefaultDKIMRotationGrace),
		recheckInterval:  envDuration("DOMAIN_RECHECK_INTERVAL", defaultRecheckInterval),
		sweepWorkers:     envInt("DOMAIN_SWEEP_CONCURRENCY", defaultSweepWorkers),
		sweepTimeout:     envDuration("DOMAIN_SWEEP_TENANT_TIMEOUT", defaultSweepTimeout),
		checkRetention:   envDuration("DOMAIN_CHECK_RETENTION", app.DefaultCheckRetention),
		port:             envInt("DOMAIN_SERVICE_PORT", defaultPort),
	}
	if len(missing) > 0 {
		return s, fmt.Errorf("faltan variables de entorno obligatorias: %s", strings.Join(missing, ", "))
	}
	if !strings.HasPrefix(s.platform.SPFInclude, "include:") {
		return s, fmt.Errorf("MAIL_SPF_INCLUDE debe ser un mecanismo include: (p. ej. include:spf.%s)", s.platformHostname)
	}
	token, err := middleware.InternalGatewayToken()
	if err != nil {
		return s, err
	}
	s.internalToken = token
	return s, nil
}

func envInt(key string, fallback int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil && v > 0 {
		return v
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v, err := time.ParseDuration(os.Getenv(key)); err == nil && v > 0 {
		return v
	}
	return fallback
}

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
		log.Fatalf("domain-service: %v", err)
	}
	keyRing, err := crypto.LoadKeyRing("MAIL_ENCRYPTION_KEY", "MAIL_ENCRYPTION_KEYS_OLD")
	if err != nil {
		log.Fatalf("domain-service: cifrado de claves DKIM: %v", err)
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
		logger.Warn("domain-service: NATS no disponible, sin eventos", zap.Error(err))
	} else {
		defer bus.Close()
		if err := bus.EnsureStream("DOMAINS", []string{"domains.>"}); err != nil {
			logger.Warn("ensure stream DOMAINS", zap.Error(err))
		}
		publisher = natsadapter.NewPublisher(bus)
	}

	uc := app.New(app.Deps{
		Repo:                 postgres.NewRepository(ctxPool),
		DNS:                  dnsadapter.New(st.dnsResolver),
		Cipher:               keyRing,
		MailDirectory:        maildirectorycli.New(st.mailDirectoryURL, st.internalToken),
		MailSecurity:         mailsecuritycli.New(st.mailSecurityURL, st.internalToken),
		Events:               publisher,
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

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RequireGatewayToken)
	r.Use(middleware.InjectFromGateway)
	r.Use(db.TenantPoolMiddleware(tenantDB))
	r.Use(middleware.SecureHeaders)
	r.Use(middleware.Logger(logger))
	r.Use(middleware.NewRateLimiter(120, time.Minute).Limit)
	r.Mount("/", handler.NewHandler(uc, authz.NewCheckerFromEnv()).Routes())

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
			total.Retired += rep.Retired
			total.Pruned += rep.Pruned
			mu.Unlock()
		})
		if err != nil {
			logger.Error("barrido de dominios: listar empresas", zap.Error(err))
			return
		}
		logger.Info("barrido de dominios completado",
			zap.Int("rechecked", total.Rechecked), zap.Int("failed", total.Failed),
			zap.Int("dkim_retired", total.Retired), zap.Int64("checks_pruned", total.Pruned),
			zap.Duration("took", time.Since(started)))
	}

	sweep()
	ticker := time.NewTicker(st.recheckInterval)
	defer ticker.Stop()
	for range ticker.C {
		sweep()
	}
}
