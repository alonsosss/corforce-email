package main

import (
	"context"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/alonsosss/corforce-email/pkg/auth"
	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/config"
	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/server"
	identityhttp "github.com/alonsosss/corforce-email/services/identity/internal/adapters/http"
	"github.com/alonsosss/corforce-email/services/identity/internal/adapters/mailerclient"
	natsadapter "github.com/alonsosss/corforce-email/services/identity/internal/adapters/nats"
	"github.com/alonsosss/corforce-email/services/identity/internal/adapters/postgres"
	"github.com/alonsosss/corforce-email/services/identity/internal/adapters/pwned"
	"github.com/alonsosss/corforce-email/services/identity/internal/app"
	"github.com/go-chi/chi/v5"
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

	eventPub := natsadapter.NewEventPublisher(bus)

	authUC := app.NewAuthUseCase(app.AuthDeps{
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
		Logger:          logger,
	})

	breachChecker := pwned.NewFromEnv()
	logger.Info("contrasenas filtradas", zap.Bool("comprobacion_activa", breachChecker.Enabled()))
	userUC := app.NewUserUseCase(userRepo, policyRepo, historyRepo, auditRepo, eventPub, breachChecker, logger)
	tenantUsersUC := app.NewTenantUsersUseCase(app.TenantUsersDeps{
		Users:       userRepo,
		TenantUsers: userRepo,
		Tenants:     tenantRepo,
		Policies:    policyRepo,
		Breach:      breachChecker,
		Audit:       auditRepo,
		Events:      eventPub,
		Logger:      logger,
	})

	mailer := mailerclient.New()
	if !mailer.Configured() {
		logger.Warn("correo transaccional sin configurar: la recuperacion de contrasena no enviara enlaces",
			zap.String("variable", mailerclient.EnvBaseURL))
	}
	publicBaseURL := os.Getenv("PUBLIC_BASE_URL")
	if publicBaseURL == "" {
		logger.Warn("PUBLIC_BASE_URL sin configurar: la recuperacion de contrasena no puede construir el enlace")
	}
	resetUC := app.NewPasswordResetUseCase(app.PasswordResetDeps{
		Users:         userRepo,
		Resets:        postgres.NewPasswordResetRepo(pool.Pool),
		Policies:      policyRepo,
		Breach:        breachChecker,
		History:       historyRepo,
		Sessions:      sessionRepo,
		Audit:         auditRepo,
		Events:        eventPub,
		Mailer:        mailer,
		Tenants:       tenantRepo,
		Logger:        logger,
		PublicBaseURL: publicBaseURL,
	})

	handler := identityhttp.NewHandler(authUC, userUC, resetUC, authz.NewCheckerFromEnv(), identityhttp.Config{
		StepUp:    tokenVerifier,
		MFAIssuer: os.Getenv("MFA_ISSUER"),
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

	port := 8001
	if p := os.Getenv("IDENTITY_PORT"); p != "" {
		if v, err := strconv.Atoi(p); err == nil {
			port = v
		}
	}

	srv := server.New(port, r, logger)
	if err := srv.Run(); err != nil {
		logger.Fatal("server error", zap.Error(err))
	}
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
