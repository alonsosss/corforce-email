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

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/config"
	"github.com/alonsosss/corforce-email/pkg/crypto"
	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/outbox"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/server"
	"github.com/alonsosss/corforce-email/pkg/tenantcell"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/adapters/dnsresolver"
	handler "github.com/alonsosss/corforce-email/services/mail-migration/internal/adapters/http"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/adapters/maildirectorycli"
	natsadapter "github.com/alonsosss/corforce-email/services/mail-migration/internal/adapters/nats"
	outboxadapter "github.com/alonsosss/corforce-email/services/mail-migration/internal/adapters/outbox"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/adapters/postgres"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/domain"
	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

const (
	defaultPort       = 8056
	defaultRunnerPort = 8057

	// Un ejecutor que no da latido en un lease pierde el trabajo; el latido es la mitad del lease.
	defaultLease         = 90 * time.Second
	defaultMaxAttempts   = 3
	defaultSweepInterval = 30 * time.Second
	// Sin limite por plan en billing (pendiente), el de la empresa es de operador.
	defaultMaxActivePerTenant = 2

	minRunnerKeyLen = 32

	mailDirectoryCellHostsEnv = "MAIL_DIRECTORY_CELL_HOSTS"
	runnerKeyEnv              = "MAIL_MIGRATION_RUNNER_KEY"
)

type settings struct {
	app              app.Config
	runnerKey        string
	port             int
	runnerPort       int
	mailDirectoryURL string
	directoryTargets *tenantcell.Targets
	internalToken    string
	perms            *authz.Checker
}

// loadSettings lee y valida la configuracion. La clave del ejecutor es opcional: sin ella el
// servicio arranca, la API lo dice (503 NOT_CONFIGURED) y no se abre el listener del ejecutor, de
// modo que desplegar el codigo no exige tener ya la clave.
func loadSettings(logger *zap.Logger) (settings, error) {
	var s settings
	var err error
	if s.port, err = config.EnvInt("MAIL_MIGRATION_PORT", defaultPort, 1, config.MaxPort); err != nil {
		return s, err
	}
	if s.runnerPort, err = config.EnvInt("MAIL_MIGRATION_RUNNER_PORT", defaultRunnerPort, 1, config.MaxPort); err != nil {
		return s, err
	}
	if s.runnerPort == s.port {
		return s, errors.New("MAIL_MIGRATION_RUNNER_PORT no puede ser el puerto de la API de administracion")
	}
	if s.app.MaxActivePerTenant, err = config.EnvInt("MAIL_MIGRATION_MAX_ACTIVE_PER_TENANT", defaultMaxActivePerTenant, 1, 100); err != nil {
		return s, err
	}
	if s.app.Lease, err = config.EnvDuration("MAIL_MIGRATION_LEASE", defaultLease, 30*time.Second, 30*time.Minute); err != nil {
		return s, err
	}
	if s.app.MaxAttempts, err = config.EnvInt("MAIL_MIGRATION_MAX_ATTEMPTS", defaultMaxAttempts, 1, 10); err != nil {
		return s, err
	}
	if s.app.SweepInterval, err = config.EnvDuration("MAIL_MIGRATION_SWEEP_INTERVAL", defaultSweepInterval, time.Second, time.Hour); err != nil {
		return s, err
	}
	if s.app.Source, err = sourcePolicyFromEnv(); err != nil {
		return s, err
	}
	if s.runnerKey = strings.TrimSpace(os.Getenv(runnerKeyEnv)); s.runnerKey != "" {
		if len(s.runnerKey) < minRunnerKeyLen {
			return s, fmt.Errorf("%s debe tener al menos %d caracteres", runnerKeyEnv, minRunnerKeyLen)
		}
		s.app.RunnerConfigured = true
	} else {
		logger.Info("migracion de buzones desactivada: falta " + runnerKeyEnv)
	}
	if s.mailDirectoryURL, err = config.RequiredServiceURL("MAIL_DIRECTORY_URL"); err != nil {
		return s, err
	}
	if s.perms, err = authz.CheckerFromEnv(); err != nil {
		return s, err
	}
	if s.internalToken, err = middleware.InternalGatewayToken(); err != nil {
		return s, err
	}
	orgURL, err := tenantcell.OrganizationURLFromEnv()
	if err != nil {
		return s, err
	}
	cells, err := tenantcell.LoadInstances(os.Getenv, tenantcell.BaseCellEnv, mailDirectoryCellHostsEnv)
	if err != nil {
		return s, err
	}
	var resolver *tenantcell.Resolver
	if cells.BaseCell != "" {
		resolver = tenantcell.NewResolver(orgURL, s.internalToken, logger)
	}
	if s.directoryTargets, err = cells.Targets(mailDirectoryCellHostsEnv, s.mailDirectoryURL, resolver); err != nil {
		return s, err
	}
	return s, nil
}

// sourcePolicyFromEnv arma las reglas del servidor de origen. Los puertos son solo los de IMAP salvo
// configuracion. El texto plano y las direcciones internas son opciones de operador: la primera
// existe para origenes que no ofrecen TLS y la segunda solo para pruebas, y se rechaza fuera de
// desarrollo y prueba porque abre la migracion a la red interna (SSRF).
func sourcePolicyFromEnv() (domain.SourcePolicy, error) {
	p := domain.SourcePolicy{Ports: []int{143, 993}}
	if raw := strings.TrimSpace(os.Getenv("MAIL_MIGRATION_SOURCE_PORTS")); raw != "" {
		p.Ports = nil
		for _, part := range strings.Split(raw, ",") {
			port, err := config.ParsePort(strings.TrimSpace(part))
			if err != nil {
				return p, fmt.Errorf("MAIL_MIGRATION_SOURCE_PORTS: %w", err)
			}
			p.Ports = append(p.Ports, port)
		}
	}
	var err error
	if p.AllowPlaintext, err = envBool("MAIL_MIGRATION_ALLOW_PLAINTEXT"); err != nil {
		return p, err
	}
	if p.AllowPrivate, err = envBool("MAIL_MIGRATION_ALLOW_PRIVATE_SOURCES"); err != nil {
		return p, err
	}
	if p.AllowPrivate && !config.DeclaredDevelopmentOrTest() {
		return p, errors.New("MAIL_MIGRATION_ALLOW_PRIVATE_SOURCES solo se admite con ENVIRONMENT development o test: abre la migracion a la red interna")
	}
	return p, nil
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
		log.Fatalf("mail-migration: %v", err)
	}
	keyRing, err := crypto.LoadKeyRing("MAIL_ENCRYPTION_KEY", "MAIL_ENCRYPTION_KEYS_OLD")
	if err != nil {
		log.Fatalf("mail-migration: cifrado de las credenciales de origen: %v", err)
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
	ctxPool := &db.ContextPool{}

	var bus *events.Bus
	if b, err := events.NewBus(cfg.NATS.URL, logger); err != nil {
		// Los eventos de auditoria esperan en la outbox de cada empresa hasta que un rele los entregue,
		// y los trabajos de un buzon borrado siguen en su base hasta que haya bus.
		logger.Warn("mail-migration: NATS no disponible, sin rele de outbox ni retirada de los trabajos de buzones borrados", zap.Error(err))
	} else {
		bus = b
		defer bus.Close()
		if err := bus.EnsureStream(outboxadapter.Stream, []string{outboxadapter.Pattern}); err != nil {
			logger.Warn("ensure stream MIGRATION", zap.Error(err))
		}
		go outbox.RunForTenants(ctx, tenantDB, db.PoolFromCtx, bus, logger, outbox.Options{})
	}

	repo := postgres.NewRepository(ctxPool)
	uc := app.New(app.Deps{
		Repo:      repo,
		Tx:        repo,
		Mailboxes: maildirectorycli.New(tenantcell.NewCaller("mail-directory", st.directoryTargets, st.internalToken, logger, tenantcell.CallerOptions{})),
		Resolver:  dnsresolver.New(),
		Cipher:    keyRing,
		Tenants:   postgres.NewTenants(tenantDB),
		Events:    outboxadapter.NewPublisher(ctxPool),
		Config:    st.app,
		Logger:    logger,
	})

	go natsadapter.NewConsumer(bus, uc, logger).Run(ctx)

	if st.app.RunnerConfigured {
		runnerSrv := &http.Server{
			Addr:              fmt.Sprintf(":%d", st.runnerPort),
			Handler:           runnerRouter(handler.NewRunnerHandler(uc, st.runnerKey, logger), logger),
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      60 * time.Second,
			IdleTimeout:       60 * time.Second,
			MaxHeaderBytes:    64 << 10,
		}
		go func() {
			logger.Info("listener del ejecutor", zap.String("addr", runnerSrv.Addr))
			if err := runnerSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Fatal("listener del ejecutor", zap.Error(err))
			}
		}()
		defer func() {
			shutdown, stop := context.WithTimeout(context.Background(), 10*time.Second)
			defer stop()
			_ = runnerSrv.Shutdown(shutdown)
		}()
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RequireGatewayToken)
	r.Use(middleware.InjectFromGateway)
	r.Use(db.TenantPoolMiddleware(tenantDB))
	r.Use(middleware.SecureHeaders)
	r.Use(middleware.Logger(logger))
	r.Use(middleware.NewRateLimiter(120, time.Minute).Limit)
	r.Mount("/", handler.NewHandler(uc, st.perms).Routes())

	srv := server.New(st.port, r, logger)
	if err := srv.Run(); err != nil {
		logger.Fatal("server error", zap.Error(err))
	}
}

// runnerRouter es la cadena del listener del ejecutor: sin gateway ni sesion, con su propia clave
// y un limite de peticiones holgado para el sondeo y los latidos de un ejecutor.
func runnerRouter(h *handler.RunnerHandler, logger *zap.Logger) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.SecureHeaders)
	r.Use(middleware.Logger(logger))
	r.Use(middleware.NewRateLimiter(600, time.Minute).Limit)
	r.Mount("/", h.Routes())
	return r
}
