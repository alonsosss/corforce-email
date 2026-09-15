package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
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
	"github.com/alonsosss/corforce-email/pkg/tenantcell"
	handler "github.com/alonsosss/corforce-email/services/mail-security/internal/adapters/http"
	natsadapter "github.com/alonsosss/corforce-email/services/mail-security/internal/adapters/nats"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/adapters/organizationcli"
	outboxadapter "github.com/alonsosss/corforce-email/services/mail-security/internal/adapters/outbox"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/adapters/postgres"
	promadapter "github.com/alonsosss/corforce-email/services/mail-security/internal/adapters/prometheus"
	redisadapter "github.com/alonsosss/corforce-email/services/mail-security/internal/adapters/redis"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/adapters/rspamd"
	smtpadapter "github.com/alonsosss/corforce-email/services/mail-security/internal/adapters/smtp"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/adapters/transactionalcli"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// Puertos: el de administracion lo enruta el gateway; los dos de los motores son los
// que la configuracion copiada de Rspamd y Postfix espera en el host mail-policy.
const (
	defaultPort       = 8042
	defaultMapsPort   = 8081
	defaultExportPort = 9081

	defaultRedisHost         = "redis"
	defaultRedisPort         = 6379
	defaultReconcileInterval = 10 * time.Minute
	defaultLogLines          = 9999
	defaultReinjectHost      = "postfix"
	defaultReinjectPort      = 590
	defaultControllerURL     = "http://rspamd:11334"
	defaultPipeMaxBodyMiB    = 50

	// outboxRetention conserva lo publicado lo mismo que el stream (EnsureStream: 7 dias).
	outboxRetention = 7 * 24 * time.Hour
	streamRetry     = 5 * time.Second
)

// Aviso de cuarentena: cadencia del barrido, vigencia de los enlaces y cerrojo de lider
// del barrido en la base de la celda (una sola instancia avisa a la vez).
const (
	defaultQuarantineNotifyInterval       = 15 * time.Minute
	defaultQuarantineLinkTTL              = 72 * time.Hour
	quarantineNotifyLockKey         int64 = 0x716e6f7469667921 // "qnotify!"
)

// Repaso de las claves DKIM de los motores: cadencia y cerrojo de lider en la base de la celda.
const (
	defaultDKIMReconcileInterval       = 15 * time.Minute
	dkimReconcileLockKey         int64 = 0x646b696d72656321 // "dkimrec!"
)

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
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

// engineListener levanta un listener HTTP interno para los motores con timeouts propios:
// /pipe recibe mensajes completos y necesita mas margen que un mapa dinamico.
func engineListener(port int, h http.Handler, readTimeout time.Duration) *http.Server {
	return &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           h,
		ReadTimeout:       readTimeout,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    64 * 1024,
	}
}

func main() {
	logger, _ := zap.NewProduction()
	defer logger.Sync()
	response.SetUnexpectedLogger(logger)

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	membership, err := tenantcell.MembershipFromEnv(logger)
	if err != nil {
		log.Fatalf("celda de la instancia: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Servicio de CELDA: un solo pool fijo a la base de la celda. La empresa viaja en
	// el contexto (InjectFromGateway) y es lo que leen las politicas RLS.
	pool, err := db.NewCellPool(ctx, cfg.Postgres, logger)
	if err != nil {
		log.Fatalf("connect cell db: %v", err)
	}
	defer pool.Close()
	ctxPool := &db.ContextPool{}
	withPool := func(c context.Context) context.Context { return db.WithPool(c, pool.Pool) }

	// Redis de los motores (no el de la plataforma): este servicio es su unico escritor. El
	// TLS es opcional y nunca exigido: los motores lo leen en claro dentro de la red de la
	// celda (deploy/mail/README.md, Contrato Redis).
	engineRedisTLS, err := config.RedisTLSFromEnv(config.EngineRedisEnvPrefix)
	if err != nil {
		log.Fatalf("redis de los motores: %v", err)
	}
	engineTLS, err := engineRedisTLS.ClientConfig()
	if err != nil {
		log.Fatalf("redis de los motores: %v", err)
	}
	store := redisadapter.New(redisadapter.Config{
		Host:     envOrDefault("MAIL_REDIS_HOST", defaultRedisHost),
		Port:     envInt("MAIL_REDIS_PORT", defaultRedisPort),
		Password: os.Getenv("MAIL_REDIS_PASSWORD"),
		TLS:      engineTLS,
	})
	defer store.Close()
	if err := store.Ping(ctx); err != nil {
		logger.Warn("redis de los motores no responde al arrancar; se reintentara en la reconciliacion", zap.Error(err))
	}

	// Los eventos se encolan en la outbox de la celda dentro de cada transaccion; sin NATS
	// esperan en la tabla y el rele los entrega cuando vuelva.
	publisher := outboxadapter.NewPublisher(ctxPool)
	var bus *events.Bus
	if b, err := events.NewBus(cfg.NATS.URL, logger); err != nil {
		logger.Warn("NATS no disponible: sin consumo del directorio; los eventos quedan en la outbox", zap.Error(err))
	} else {
		bus = b
		defer bus.Close()
		go runCellRelay(ctx, bus, pool, logger)
	}

	directory := postgres.NewDirectoryRepository(ctxPool)
	policyReader := postgres.NewPolicyReader(ctxPool)
	quarantineRepo := postgres.NewQuarantineRepository(ctxPool)
	redisSync := app.NewRedisSync(store, directory, policyReader, logger)

	// Claves DKIM solo de dominios activos en esta celda cuya empresa sigue existiendo.
	// ORGANIZATION_URL y el token interno ya los valido MembershipFromEnv.
	orgToken, err := middleware.InternalGatewayToken()
	if err != nil {
		log.Fatalf("claves DKIM: %v", err)
	}
	dkimUC := app.NewDKIMUseCase(app.DKIMDeps{
		Directory: directory, Lock: postgres.NewDKIMLock(ctxPool), Sync: redisSync, Logger: logger,
		Tenants: organizationcli.New(tenantcell.NewResolver(strings.TrimSpace(os.Getenv("ORGANIZATION_URL")), orgToken, logger)),
		Metrics: promadapter.New(),
	})

	// Enlaces sin sesion del aviso de cuarentena: firmados con MAIL_LINK_SIGNING_KEY,
	// colgados de PUBLIC_BASE_URL y atados a la celda CELL_CODE, que va en su ruta para que el
	// gateway los enrute. Sin ellos el servicio arranca, pero no avisa y todo enlace es
	// invalido.
	noticeRepo := postgres.NewQuarantineNoticeRepository(ctxPool)
	quarantineLinks, linksErr := domain.NewQuarantineLinkSigner(os.Getenv("MAIL_LINK_SIGNING_KEY"), os.Getenv("PUBLIC_BASE_URL"),
		strings.TrimSpace(os.Getenv("CELL_CODE")), envDuration("MAIL_QUARANTINE_LINK_TTL", defaultQuarantineLinkTTL))
	if linksErr != nil {
		logger.Error("aviso de cuarentena y sus enlaces desactivados", zap.Error(linksErr))
	}

	policyUC := app.NewPolicyUseCase(app.PolicyDeps{
		Tx: ctxPool, Repo: postgres.NewPolicyRepository(ctxPool), Directory: directory, Sync: redisSync, Logger: logger,
	})
	quarantineUC := app.NewQuarantineUseCase(app.QuarantineDeps{
		Tx:   ctxPool,
		Repo: quarantineRepo,
		Reinjector: smtpadapter.New(
			net.JoinHostPort(envOrDefault("MAIL_QUARANTINE_REINJECT_HOST", defaultReinjectHost), strconv.Itoa(envInt("MAIL_QUARANTINE_REINJECT_PORT", defaultReinjectPort))),
			envOrDefault("MAIL_HOSTNAME", "mail-security")),
		Learner: rspamd.New(envOrDefault("RSPAMD_CONTROLLER_URL", defaultControllerURL), os.Getenv("RSPAMD_CONTROLLER_PASSWORD")),
		Events:  publisher,
		Notices: noticeRepo,
		Links:   quarantineLinks,
		Logger:  logger,
	})
	engineUC := app.NewEngineUseCase(app.EngineDeps{
		Tx: ctxPool, Documents: postgres.NewDocumentRepository(ctxPool),
		Directory: directory, Policy: policyReader, Quarantine: quarantineRepo, Sync: redisSync,
		Store: store, Events: publisher, Logger: logger, LogLines: int64(envInt("MAIL_LOG_LINES", defaultLogLines)),
	})
	firewallUC := app.NewFirewallUseCase(app.FirewallDeps{
		Tx: ctxPool, Repo: postgres.NewFirewallRepository(ctxPool), Policy: policyReader, Sync: redisSync, Store: store, Logger: logger,
	})

	// Reconciliacion de Redis al arrancar y periodica, y consumo de eventos del
	// directorio. Ambos corren fuera de cualquier peticion: el pool va en el contexto.
	go app.NewReconciler(redisSync, policyReader, quarantineRepo,
		envDuration("MAIL_REDIS_RECONCILE_INTERVAL", defaultReconcileInterval), logger).Run(withPool(ctx))
	go natsadapter.NewDirectoryConsumer(bus, redisSync, dkimUC, policyReader, withPool, logger).Run(ctx)
	go dkimUC.RunReconciler(withPool(ctx), envDuration("MAIL_DKIM_RECONCILE_INTERVAL", defaultDKIMReconcileInterval),
		func(c context.Context) (func(), bool) { return db.TryLeaderLock(c, pool.Pool, dkimReconcileLockKey) })

	// Aviso de cuarentena por transactional (POST /internal/transactional/messages con
	// purpose=quarantine_notice), con el cerrojo de lider de la celda.
	transactionalURL := strings.TrimSpace(os.Getenv("TRANSACTIONAL_URL"))
	internalToken := os.Getenv("INTERNAL_GATEWAY_TOKEN")
	switch {
	case linksErr != nil:
		// Ya registrado al construir el firmante: sin enlaces no se avisa.
	case transactionalURL == "" || internalToken == "":
		logger.Error("aviso de cuarentena desactivado: faltan TRANSACTIONAL_URL o INTERNAL_GATEWAY_TOKEN")
	default:
		notifier := app.NewQuarantineNotifier(app.NotifierDeps{
			Tx: ctxPool, Policy: policyReader, Notices: noticeRepo, Directory: directory,
			Sender: transactionalcli.New(transactionalURL, internalToken), Links: quarantineLinks,
			Interval: envDuration("MAIL_QUARANTINE_NOTIFY_INTERVAL", defaultQuarantineNotifyInterval), Logger: logger,
		})
		go notifier.Run(withPool(ctx), func(c context.Context) (func(), bool) {
			return db.TryLeaderLock(c, pool.Pool, quarantineNotifyLockKey)
		})
	}

	// Superficie A: API de administracion tras el gateway (y rutas internas con token). El
	// cortafuegos es de plataforma: el superadmin lo opera en cualquier celda con celda destino.
	routes := handler.NewHandler(policyUC, quarantineUC, firewallUC, dkimUC, authz.NewCheckerFromEnv()).Routes()
	if err := membership.AcceptOperators(routes, handler.PlatformRoutes()); err != nil {
		log.Fatalf("celda de la instancia: %v", err)
	}
	r := apiRouter(pool.Pool, membership, routes, logger)

	// Superficie B: listeners de los motores, sin gateway ni JWT, acotados por IP. No llevan
	// empresa (Postfix, Dovecot y Rspamd no saben de ellas): no pasan por Membership.
	allowedCIDRs := os.Getenv("MAIL_ENGINE_ALLOWED_CIDRS")
	pipeMaxBody := int64(envInt("MAIL_QUARANTINE_MAX_BODY_MB", defaultPipeMaxBodyMiB)) * 1024 * 1024
	maps := engineListener(envInt("MAIL_POLICY_MAPS_PORT", defaultMapsPort),
		db.StaticPoolMiddleware(pool.Pool)(handler.NewEngineHandler(engineUC, logger).Routes(allowedCIDRs)), 15*time.Second)
	export := engineListener(envInt("MAIL_POLICY_EXPORT_PORT", defaultExportPort),
		db.StaticPoolMiddleware(pool.Pool)(handler.NewExporterHandler(engineUC, pipeMaxBody, logger).Routes(allowedCIDRs)), 120*time.Second)
	for _, srv := range []*http.Server{maps, export} {
		go func(s *http.Server) {
			logger.Info("engine listener starting", zap.String("addr", s.Addr))
			if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Fatal("engine listener failed", zap.String("addr", s.Addr), zap.Error(err))
			}
		}(srv)
	}

	port := envInt("MAIL_SECURITY_PORT", defaultPort)
	srv := server.New(port, r, logger)
	runErr := srv.Run()

	// El servidor principal ya atendio la senal de parada: se apagan los listeners de
	// los motores con el mismo margen y se detienen las tareas de fondo.
	cancel()
	shutdownCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	for _, s := range []*http.Server{maps, export} {
		if err := s.Shutdown(shutdownCtx); err != nil {
			logger.Warn("engine listener shutdown", zap.String("addr", s.Addr), zap.Error(err))
		}
	}
	if runErr != nil {
		logger.Fatal("server error", zap.Error(runErr))
	}
}

// apiRouter monta la superficie A: el API de administracion que llega por el gateway, los
// enlaces publicos de cuarentena y las rutas internas de domain-service, todo tras el token
// interno. Una peticion por una empresa que no es de esta celda se rechaza antes de llegar a
// ninguna ruta (tenantcell.Membership).
func apiRouter(pool *pgxpool.Pool, membership *tenantcell.Membership, routes http.Handler, logger *zap.Logger) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RequireGatewayToken)
	r.Use(middleware.InjectFromGateway)
	r.Use(db.StaticPoolMiddleware(pool))
	r.Use(middleware.SecureHeaders)
	r.Use(middleware.Logger(logger))
	r.Use(middleware.NewRateLimiter(120, time.Minute).Limit)
	r.Use(membership.Require)
	r.Mount("/", routes)
	return r
}

// runCellRelay vacia la outbox de la celda hacia JetStream. Antes asegura el stream de sus
// subjects, reintentando mientras NATS no responda: publicar en un subject sin stream falla
// y consume los reintentos de la fila. Comparte cerrojo con el rele de mail-directory, de
// modo que en toda la celda solo vacia una instancia a la vez.
func runCellRelay(ctx context.Context, bus *events.Bus, pool *db.Pool, logger *zap.Logger) {
	for {
		err := bus.EnsureStream(domain.StreamName, []string{domain.SubjectQuarantineStored, domain.SubjectQuarantineReleased})
		if err == nil {
			break
		}
		logger.Warn("stream MAIL_SECURITY no asegurado; se reintenta", zap.Error(err))
		select {
		case <-ctx.Done():
			return
		case <-time.After(streamRetry):
		}
	}
	relay := outbox.NewRelay(pool.Pool, bus, logger, outbox.Options{Retention: outboxRetention})
	relay.RunExclusive(ctx, func(c context.Context) (func(), bool) {
		return db.TryLeaderLock(c, pool.Pool, outbox.CellRelayLockKey)
	})
}
