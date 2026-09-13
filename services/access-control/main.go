package main

import (
	"context"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/alonsosss/corforce-email/pkg/config"
	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/server"
	handler "github.com/alonsosss/corforce-email/services/access-control/internal/adapters/http"
	natsadapter "github.com/alonsosss/corforce-email/services/access-control/internal/adapters/nats"
	"github.com/alonsosss/corforce-email/services/access-control/internal/adapters/postgres"
	redisadapter "github.com/alonsosss/corforce-email/services/access-control/internal/adapters/redis"
	"github.com/alonsosss/corforce-email/services/access-control/internal/app"
	"github.com/alonsosss/corforce-email/services/access-control/internal/ports"
	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

func main() {
	logger, _ := zap.NewProduction()
	defer logger.Sync()
	response.SetUnexpectedLogger(logger)

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
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
	if err := rdb.Ping(ctx).Err(); err != nil {
		logger.Warn("redis unavailable, running without policy cache", zap.Error(err))
		rdb.Close()
	} else {
		cachedRepo := redisadapter.NewCachedUserRoleRepo(userRoleRepo, rdb)
		userRoles, policyCache = cachedRepo, cachedRepo
	}
	rbacUC := app.NewRBACUseCase(roleRepo, permRepo, rolePermRepo, userRoles, denialRepo, moduleGate, systemRoles, logger)
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
		logger.Warn("NATS no disponible: las bajas de cuentas no retiraran sus roles", zap.Error(err))
	} else {
		defer bus.Close()
		go natsadapter.NewIdentityConsumer(bus, deletedAccounts, logger).Run(ctx)
	}

	h := handler.NewHandler(rbacUC)

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RequireGatewayToken)
	r.Use(middleware.InjectFromGateway)
	r.Use(middleware.SecureHeaders)
	r.Use(middleware.Logger(logger))
	limiter := middleware.NewRateLimiter(100, time.Minute)
	r.Use(limiter.Limit)
	r.Mount("/", h.Routes())
	// Interno: el ciclo de vida de los roles de una empresa entera, que orquesta
	// organization. Mismo token interno; ninguna persona llega aqui.
	r.Mount("/internal/access-control", handler.NewInternalHandler(tenantRolesUC).Routes())

	port := 8002
	if p := os.Getenv("ACCESS_CONTROL_PORT"); p != "" {
		if v, err := strconv.Atoi(p); err == nil {
			port = v
		}
	}

	srv := server.New(port, r, logger)
	if err := srv.Run(); err != nil {
		logger.Fatal("server error", zap.Error(err))
	}
}
