package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/alonsosss/corforce-email/pkg/auth"
	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/config"
	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/outbox"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/server"
	identityhttp "github.com/alonsosss/corforce-email/services/identity/internal/adapters/http"
	"github.com/alonsosss/corforce-email/services/identity/internal/adapters/mailerclient"
	natsadapter "github.com/alonsosss/corforce-email/services/identity/internal/adapters/nats"
	outboxadapter "github.com/alonsosss/corforce-email/services/identity/internal/adapters/outbox"
	"github.com/alonsosss/corforce-email/services/identity/internal/adapters/passwordhash"
	"github.com/alonsosss/corforce-email/services/identity/internal/adapters/postgres"
	"github.com/alonsosss/corforce-email/services/identity/internal/adapters/pwned"
	"github.com/alonsosss/corforce-email/services/identity/internal/adapters/resetqueue"
	"github.com/alonsosss/corforce-email/services/identity/internal/app"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

func main() {
	logger, _ := zap.NewProduction()
	// Sin esto, un 500 sale sin dejar constancia del motivo y no hay forma de saber que
	// fallo por debajo.
	response.SetUnexpectedLogger(logger)
	defer logger.Sync()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	port, err := config.EnvInt("IDENTITY_PORT", defaultPort, 1, config.MaxPort)
	if err != nil {
		log.Fatal(err)
	}
	perms, err := authz.CheckerFromEnv()
	if err != nil {
		log.Fatal(err)
	}
	// Opcional: sin ella identity autentica igual, pero la recuperacion de contrasena no envia.
	mailerURL, err := config.ServiceURL(mailerclient.EnvBaseURL, "")
	if err != nil {
		log.Fatal(err)
	}
	// Antes de abrir nada: sin clave de firma valida no hay servicio que levantar.
	tokenSvc, tokenVerifier, err := newTokenService(cfg, logger)
	if err != nil {
		log.Fatalf("token keys: %v", err)
	}

	ctx := context.Background()

	pool, err := db.NewPool(ctx, cfg.Postgres.DSN(), logger)
	if err != nil {
		log.Fatalf("connect db: %v", err)
	}
	defer pool.Close()

	bus, err := events.NewBus(cfg.NATS.URL, logger)
	if err != nil {
		log.Fatalf("connect nats: %v", err)
	}
	defer bus.Close()

	userRepo := postgres.NewUserRepo(pool.Pool)
	sessionRepo := postgres.NewSessionRepo(pool.Pool)
	blocklistRepo := postgres.NewTokenBlocklistRepo(pool.Pool)
	policyRepo := postgres.NewPasswordPolicyRepo(pool.Pool)
	sessionPolicyRepo := postgres.NewSessionPolicyRepo(pool.Pool)
	historyRepo := postgres.NewPasswordHistoryRepo(pool.Pool)
	auditRepo := postgres.NewAuditRepo(pool.Pool)
	tenantRepo := postgres.NewTenantRepo(pool.Pool)
	roleRepo := postgres.NewRoleRepo(pool.Pool)
	unknownLoginRepo := postgres.NewUnknownLoginRepo(pool.Pool)

	eventPub := natsadapter.NewEventPublisher(bus)

	hasher, err := passwordhash.NewBcrypt(passwordhash.BcryptCost)
	if err != nil {
		log.Fatalf("password hasher: %v", err)
	}
	authUC, err := app.NewAuthUseCase(app.AuthDeps{
		Users:           userRepo,
		Sessions:        sessionRepo,
		Blocklist:       blocklistRepo,
		Policies:        policyRepo,
		SessionPolicies: sessionPolicyRepo,
		History:         historyRepo,
		Audit:           auditRepo,
		Events:          eventPub,
		Tokens:          tokenSvc,
		Tenants:         tenantRepo,
		Roles:           roleRepo,
		Hasher:          hasher,
		UnknownLogins:   unknownLoginRepo,
		Logger:          logger,
	})
	if err != nil {
		log.Fatalf("auth use case: %v", err)
	}

	breachChecker := pwned.NewFromEnv()
	logger.Info("contrasenas filtradas", zap.Bool("comprobacion_activa", breachChecker.Enabled()))
	// La baja de una cuenta y su evento se confirman juntos: el publicador escribe en la
	// outbox del registro por la transaccion que abre el Transactor.
	userUC := app.NewUserUseCase(app.UserDeps{
		Users:         userRepo,
		Policies:      policyRepo,
		History:       historyRepo,
		Audit:         auditRepo,
		Events:        eventPub,
		Breach:        breachChecker,
		Hasher:        hasher,
		Tx:            postgres.NewTransactor(pool.Pool),
		AccountEvents: outboxadapter.NewPublisher(&db.ContextPool{}),
		Logger:        logger,
	})
	go runAccountEventRelay(ctx, bus, pool.Pool, logger)
	tenantUsersUC := app.NewTenantUsersUseCase(app.TenantUsersDeps{
		Users:       userRepo,
		TenantUsers: userRepo,
		Tenants:     tenantRepo,
		Policies:    policyRepo,
		Breach:      breachChecker,
		Hasher:      hasher,
		Audit:       auditRepo,
		Events:      eventPub,
		Logger:      logger,
	})

	platformTenantID := uuid.Nil
	if raw := os.Getenv(mailerclient.EnvPlatformTenantID); raw != "" {
		if platformTenantID, err = uuid.Parse(raw); err != nil {
			log.Fatalf("%s no es un uuid valido: %v", mailerclient.EnvPlatformTenantID, err)
		}
	} else {
		logger.Warn("sin la empresa de plataforma, los correos del sistema salen como la empresa de cada usuario y solo funcionan si su dominio de remitente esta verificado alli",
			zap.String("variable", mailerclient.EnvPlatformTenantID))
	}
	mailer := mailerclient.New(mailerURL, os.Getenv("INTERNAL_GATEWAY_TOKEN"), platformTenantID)
	if !mailer.Configured() {
		logger.Warn("correo transaccional sin configurar: la recuperacion de contrasena no enviara enlaces",
			zap.String("variable", mailerclient.EnvBaseURL))
	}
	publicBaseURL := os.Getenv("PUBLIC_BASE_URL")
	if publicBaseURL == "" {
		logger.Warn("PUBLIC_BASE_URL sin configurar: la recuperacion de contrasena no puede construir el enlace")
	}
	resetQueue, err := resetqueue.New(resetqueue.Options{
		Capacity: resetQueueCapacity, Workers: resetWorkers, JobTimeout: resetJobTimeout, DrainTimeout: resetDrainTimeout,
	}, logger)
	if err != nil {
		log.Fatalf("password reset queue: %v", err)
	}
	resetUC, err := app.NewPasswordResetUseCase(app.PasswordResetDeps{
		Users:         userRepo,
		Resets:        postgres.NewPasswordResetRepo(pool.Pool),
		Policies:      policyRepo,
		Breach:        breachChecker,
		Hasher:        hasher,
		History:       historyRepo,
		Sessions:      sessionRepo,
		Audit:         auditRepo,
		Events:        eventPub,
		Mailer:        mailer,
		Tenants:       tenantRepo,
		Queue:         resetQueue,
		Logger:        logger,
		PublicBaseURL: publicBaseURL,
	})
	if err != nil {
		log.Fatalf("password reset use case: %v", err)
	}
	// El trabajo de fondo que depende de la base se para despues del servidor HTTP: lo que la
	// cola ya acepto se atiende antes de cerrar el pool.
	workCtx, stopWork := context.WithCancel(ctx)
	resetDone := make(chan struct{})
	go func() {
		defer close(resetDone)
		resetQueue.Run(workCtx, resetUC.ProcessReset)
	}()
	go runUnknownLoginPrune(workCtx, unknownLoginRepo, logger)

	handler := identityhttp.NewHandler(authUC, userUC, resetUC, perms, identityhttp.Config{
		StepUp:           tokenVerifier,
		MFAIssuer:        os.Getenv("MFA_ISSUER"),
		RefreshCookieTTL: cfg.JWT.RefreshTTL,
	})

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RequireGatewayToken)
	r.Use(middleware.InjectFromGateway)
	r.Use(middleware.SecureHeaders)
	r.Use(middleware.Logger(logger))
	limiter := middleware.NewRateLimiter(60, time.Minute)
	r.Use(limiter.Limit)
	r.Mount("/", handler.Routes())
	// Interno: las cuentas de una empresa entera, que orquesta organization al darla de
	// alta y de baja. Mismo token interno; ninguna persona llega aqui.
	r.Mount("/internal/identity", identityhttp.NewInternalHandler(tenantUsersUC).Routes())

	srv := server.New(port, r, logger)
	if err := srv.Run(); err != nil {
		logger.Fatal("server error", zap.Error(err))
	}
	stopWork()
	<-resetDone
}

const (
	defaultPort = 8001

	// streamRetry espacia los intentos de declarar el stream mientras NATS no responde.
	streamRetry = 5 * time.Second
	// outboxRetention conserva lo publicado lo mismo que el stream (EnsureStream: 7 dias).
	outboxRetention = 7 * 24 * time.Hour

	// La cola de "olvide mi contrasena" acota su memoria; lo que llega a ella lo limitan antes
	// el cupo estricto del gateway (AUTH_RATE_LIMIT_PER_MIN) y el de identity, por IP.
	resetQueueCapacity = 256
	resetWorkers       = 4
	// resetJobTimeout cubre la busqueda, el enlace y la llamada a transactional (15 s en
	// mailerclient); resetDrainTimeout es cuanto se atiende lo encolado al apagar.
	resetJobTimeout   = 30 * time.Second
	resetDrainTimeout = 10 * time.Second

	// Los contadores de correos sin cuenta olvidados se borran cada hora, en lotes.
	unknownLoginPruneEvery = time.Hour
	unknownLoginPruneBatch = 1000
)

// runUnknownLoginPrune borra los contadores de correos sin cuenta que ya no cuentan: sin ella,
// cada correo inventado que alguien prueba dejaria su fila para siempre. Borrarlos no cambia
// ninguna respuesta (domain.FailedLoginWindow).
func runUnknownLoginPrune(ctx context.Context, repo *postgres.UnknownLoginRepo, logger *zap.Logger) {
	t := time.NewTicker(unknownLoginPruneEvery)
	defer t.Stop()
	for {
		for {
			n, err := repo.PruneForgotten(ctx, time.Now(), unknownLoginPruneBatch)
			if err != nil {
				if ctx.Err() == nil {
					logger.Warn("no se pudieron podar los contadores de correos sin cuenta", zap.Error(err))
				}
				break
			}
			if n < unknownLoginPruneBatch {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// runAccountEventRelay declara el stream IDENTITY y despues vacia la outbox del registro. Sin
// NATS reintenta: los eventos esperan en la outbox, no se pierden. billing vacia la misma
// tabla con su propio rele; FOR UPDATE SKIP LOCKED reparte las filas y el id del evento
// deduplica en JetStream.
func runAccountEventRelay(ctx context.Context, bus *events.Bus, pool *pgxpool.Pool, logger *zap.Logger) {
	for {
		err := bus.EnsureStream(outboxadapter.StreamName, []string{outboxadapter.SubjectUserDeleted})
		if err == nil {
			break
		}
		logger.Warn("no se pudo declarar el stream IDENTITY; se reintenta", zap.Error(err))
		select {
		case <-ctx.Done():
			return
		case <-time.After(streamRetry):
		}
	}
	outbox.NewRelay(pool, bus, logger, outbox.Options{Retention: outboxRetention}).Run(ctx)
}

// newTokenService resuelve la clave de firma (JWT_SIGNING_KEY, solo identity la recibe) y
// el verificador de los tokens que identity emite y vuelve a leer (reto MFA y step-up).
func newTokenService(cfg *config.Config, logger *zap.Logger) (*auth.TokenService, *auth.Verifier, error) {
	signer, ephemeral, err := auth.SignerFromEnv(cfg.JWT.AllowEphemeralSigningKey)
	if err != nil {
		return nil, nil, err
	}
	keys, err := auth.IssuerKeySet(signer, os.Getenv(auth.EnvPublicKeys))
	if err != nil {
		return nil, nil, err
	}
	verifier, err := auth.NewVerifier(keys)
	if err != nil {
		return nil, nil, err
	}
	if ephemeral {
		// La clave publica no es secreta: se registra para poder publicarla al gateway
		// en una prueba local. La privada muere con el proceso y nunca se escribe.
		public, err := auth.EncodePublicKey(signer.PublicKey())
		if err != nil {
			return nil, nil, err
		}
		logger.Warn("identity firma con un par efimero: solo desarrollo o prueba, los tokens mueren con el proceso",
			zap.String("jwt_public_keys", signer.KID()+":"+public))
	} else {
		logger.Info("clave de firma del token de acceso", zap.String("kid", signer.KID()),
			zap.Strings("claves_aceptadas", keys.KIDs()))
	}
	return auth.NewTokenService(signer, verifier, cfg.JWT.AccessTTL, cfg.JWT.RefreshTTL), verifier, nil
}
