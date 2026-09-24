package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/auth"
	"github.com/alonsosss/corforce-email/pkg/config"
	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/outbox"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/server"
	hmacadapter "github.com/alonsosss/corforce-email/services/access-control/internal/adapters/hmac"
	handler "github.com/alonsosss/corforce-email/services/access-control/internal/adapters/http"
	natsadapter "github.com/alonsosss/corforce-email/services/access-control/internal/adapters/nats"
	"github.com/alonsosss/corforce-email/services/access-control/internal/adapters/postgres"
	promadapter "github.com/alonsosss/corforce-email/services/access-control/internal/adapters/prometheus"
	redisadapter "github.com/alonsosss/corforce-email/services/access-control/internal/adapters/redis"
	"github.com/alonsosss/corforce-email/services/access-control/internal/app"
	"github.com/alonsosss/corforce-email/services/access-control/internal/ports"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

const (
	defaultPort = 8002
	// outboxRetention: lo publicado se poda pasado este tiempo (identity y billing vacian la misma
	// tabla con la misma retencion).
	outboxRetention = 7 * 24 * time.Hour
	streamRetry     = 10 * time.Second
)

func main() {
	logger, _ := zap.NewProduction()
	defer logger.Sync()
	response.SetUnexpectedLogger(logger)

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	port, err := config.EnvInt("ACCESS_CONTROL_PORT", defaultPort, 1, config.MaxPort)
	if err != nil {
		log.Fatal(err)
	}
	hasher, err := apiKeyHasherFromEnv()
	if err != nil {
		log.Fatal(err)
	}
	stepUp, err := stepUpFromEnv()
	if err != nil {
		log.Fatal(err)
	}
	smtpSettings, err := smtpSettingsFromEnv()
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()

	pool, err := db.NewPool(ctx, cfg.Postgres.DSN(), logger)
	if err != nil {
		log.Fatalf("connect db: %v", err)
	}
	defer pool.Close()

	roleRepo := postgres.NewRoleRepo(pool.Pool)
	permRepo := postgres.NewPermissionRepo(pool.Pool)
	rolePermRepo := postgres.NewRolePermissionRepo(pool.Pool)
	userRoleRepo := postgres.NewUserRoleRepo(pool.Pool)
	denialRepo := postgres.NewDenialRepo(pool.Pool)
	moduleGate := postgres.NewTenantModuleGateRepo(pool.Pool)

	redisTLS, err := cfg.Redis.TLSConfig()
	if err != nil {
		log.Fatalf("redis: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{
		Addr:      cfg.Redis.Addr(),
		Password:  cfg.Redis.Password,
		DB:        0,
		TLSConfig: redisTLS,
	})

	// Los nombres de los roles estructurales llegan del paquete compartido: son los
	// mismos que reconoce el gateway, y tenant_admin es el que este servicio siembra en
	// cada empresa nueva cuando organization se lo pide.
	systemRoles := app.SystemRoles{
		Superadmin:  middleware.RoleSuperadmin,
		TenantAdmin: middleware.RoleTenantAdmin,
	}

	// Sin Redis el servicio sigue funcionando: la politica se resuelve en cada consulta
	// contra la base, sin cache.
	var userRoles ports.UserRoleRepository = userRoleRepo
	var policyCache ports.PolicyCache
	var revocations ports.APIKeyRevocations
	if err := rdb.Ping(ctx).Err(); err != nil {
		logger.Warn("redis unavailable, running without policy cache; las claves revocadas dejan de valer al vencer las caches", zap.Error(err))
		rdb.Close()
	} else {
		cachedRepo := redisadapter.NewCachedUserRoleRepo(userRoleRepo, rdb)
		userRoles, policyCache = cachedRepo, cachedRepo
		revocations = redisadapter.NewAPIKeyRevocations(rdb, logger)
	}
	rbacUC := app.NewRBACUseCase(roleRepo, permRepo, rolePermRepo, userRoles, denialRepo, moduleGate, systemRoles, policyCache, logger)
	tenantRolesUC := app.NewTenantRolesUseCase(app.TenantRolesDeps{
		Lifecycle:   postgres.NewTenantRoleLifecycleRepo(pool.Pool),
		Roles:       roleRepo,
		UserRoles:   userRoles,
		Cache:       policyCache,
		Tenants:     postgres.NewTenantDirectory(pool.Pool),
		SystemRoles: systemRoles,
	})

	// Sin NATS el servicio responde igual: las bajas de cuentas esperan en el stream IDENTITY
	// y sus roles se retiran cuando el consumidor consigue suscribirse.
	deletedAccounts := app.NewDeletedAccountsUseCase(postgres.NewDeletedAccountRepo(pool.Pool), policyCache)
	if bus, err := events.NewBus(cfg.NATS.URL, logger); err != nil {
		logger.Warn("NATS no disponible: las bajas de cuentas no retiraran sus roles y los eventos de claves esperan en la outbox", zap.Error(err))
	} else {
		defer bus.Close()
		go natsadapter.NewIdentityConsumer(bus, deletedAccounts, logger).Run(ctx)
		go runAPIKeyEventRelay(ctx, bus, pool.Pool, logger)
	}

	apiKeysUC := app.NewAPIKeysUseCase(app.APIKeysDeps{
		Keys:        postgres.NewAPIKeyRepo(pool.Pool),
		Hasher:      hasher,
		Users:       userRoles,
		Modules:     moduleGate,
		Tenants:     postgres.NewTenantDirectory(pool.Pool),
		Revocations: revocations,
		Metrics:     promadapter.New(),
		SystemRoles: systemRoles,
		Random:      rand.Reader,
		Logger:      logger,
	})
	apiKeys := handler.NewAPIKeysHandler(apiKeysUC, rbacUC, stepUp, smtpSettings)

	h := handler.NewHandler(rbacUC)

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RequireGatewayToken)
	r.Use(middleware.InjectFromGateway)
	r.Use(middleware.SecureHeaders)
	r.Use(middleware.Logger(logger))
	limiter := middleware.NewRateLimiter(100, time.Minute)
	r.Use(limiter.Limit)
	r.Mount("/api/v1/access/api-keys", apiKeys.Routes())
	r.Mount("/", h.Routes())
	// Interno: el ciclo de vida de los roles de una empresa entera, que orquesta
	// organization. Mismo token interno; ninguna persona llega aqui.
	r.Mount("/internal/access-control", handler.NewInternalHandler(tenantRolesUC).Routes())
	// La resolucion de claves de API la piden el gateway y smtp-relay: tiene su propio cupo,
	// fuera del general por IP, porque todas las peticiones llegan desde esos dos servicios y ya
	// vienen espaciadas por su cache.
	internalAPIKeys := chi.NewRouter()
	internalAPIKeys.Use(middleware.RequestID, middleware.RequireGatewayToken, middleware.InjectFromGateway, middleware.Logger(logger))
	internalAPIKeys.Mount("/", apiKeys.InternalRoutes())
	root := chi.NewRouter()
	root.Mount("/internal/access-control/api-keys", internalAPIKeys)
	root.Mount("/", r)

	srv := server.New(port, root, logger)
	if err := srv.Run(); err != nil {
		logger.Fatal("server error", zap.Error(err))
	}
}

// apiKeyHasherFromEnv exige API_KEY_HASH_KEY fuera de development y test: sin ella no se crea
// ni se resuelve ninguna clave. En desarrollo sin llave el servicio arranca y las claves
// responden como no disponibles.
func apiKeyHasherFromEnv() (ports.APIKeyHasher, error) {
	h, err := hmacadapter.FromEnv()
	switch {
	case err == nil:
		return h, nil
	case errors.Is(err, hmacadapter.ErrNoKey) && config.DeclaredDevelopmentOrTest():
		return unavailableHasher{}, nil
	}
	return nil, fmt.Errorf("claves de API: %w", err)
}

// unavailableHasher es el de un entorno de desarrollo sin llave: toda operacion falla.
type unavailableHasher struct{}

func (unavailableHasher) Hash([]byte) ([]byte, string, error) { return nil, "", hmacadapter.ErrNoKey }
func (unavailableHasher) Verify([]byte, []byte, string) (bool, bool, error) {
	return false, false, hmacadapter.ErrNoKey
}

// stepUpFromEnv: crear una clave de API es dar una credencial duradera, asi que pide reconfirmar
// la identidad con STEP_UP_MODE=enforce (el mismo criterio que domain-service al guardar la
// credencial de un proveedor DNS).
func stepUpFromEnv() (func(http.Handler) http.Handler, error) {
	if !strings.EqualFold(os.Getenv("STEP_UP_MODE"), "enforce") {
		return middleware.RequireStepUp(nil), nil
	}
	keys, err := auth.KeySetFromEnv()
	if err != nil {
		return nil, fmt.Errorf("STEP_UP_MODE=enforce: access-control verifica el step-up de crear una clave de API: %w", err)
	}
	verifier, err := auth.NewVerifier(keys)
	if err != nil {
		return nil, fmt.Errorf("STEP_UP_MODE=enforce: %w", err)
	}
	return middleware.RequireStepUp(verifier), nil
}

// smtpSettingsFromEnv lee la direccion publica del relay SMTP que ve la empresa. Sin
// SMTP_RELAY_PUBLIC_HOST la pagina de claves no ofrece SMTP.
func smtpSettingsFromEnv() (handler.SMTPSettings, error) {
	host := strings.TrimSpace(os.Getenv("SMTP_RELAY_PUBLIC_HOST"))
	if host == "" {
		return handler.SMTPSettings{}, nil
	}
	if !config.ValidHost(host) {
		return handler.SMTPSettings{}, fmt.Errorf("SMTP_RELAY_PUBLIC_HOST=%q no es un nombre de host", host)
	}
	starttls, err := config.EnvInt("SMTP_RELAY_PUBLIC_STARTTLS_PORT", 2525, 1, config.MaxPort)
	if err != nil {
		return handler.SMTPSettings{}, err
	}
	implicit, err := config.EnvInt("SMTP_RELAY_PUBLIC_TLS_PORT", 2465, 1, config.MaxPort)
	if err != nil {
		return handler.SMTPSettings{}, err
	}
	return handler.SMTPSettings{Host: host, StartTLSPort: starttls, TLSPort: implicit}, nil
}

// runAPIKeyEventRelay declara el stream de los eventos de claves y vacia la outbox del registro.
// identity y billing vacian la misma tabla; FOR UPDATE SKIP LOCKED reparte las filas y el id del
// evento deduplica en JetStream.
func runAPIKeyEventRelay(ctx context.Context, bus *events.Bus, pool *pgxpool.Pool, logger *zap.Logger) {
	for {
		err := bus.EnsureStream(postgres.APIKeyStream, postgres.APIKeySubjects())
		if err == nil {
			break
		}
		logger.Warn("no se pudo declarar el stream ACCESS; se reintenta", zap.Error(err))
		select {
		case <-ctx.Done():
			return
		case <-time.After(streamRetry):
		}
	}
	outbox.NewRelay(pool, bus, logger, outbox.Options{Retention: outboxRetention}).Run(ctx)
}
