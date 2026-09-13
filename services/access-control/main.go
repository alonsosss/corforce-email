package main

import (
	"context"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/alonsosss/corforce-email/pkg/config"
	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/server"
	handler "github.com/alonsosss/corforce-email/services/access-control/internal/adapters/http"
	"github.com/alonsosss/corforce-email/services/access-control/internal/adapters/postgres"
	redisadapter "github.com/alonsosss/corforce-email/services/access-control/internal/adapters/redis"
	"github.com/alonsosss/corforce-email/services/access-control/internal/app"
	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

func main() {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

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
	// mismos que reconoce el gateway y los que siembra organization al crear el tenant.
	systemRoles := app.SystemRoles{
		Superadmin:  middleware.RoleSuperadmin,
		TenantAdmin: middleware.RoleTenantAdmin,
	}

	// Sin Redis el servicio sigue funcionando: la politica se resuelve en cada consulta
	// contra la base, sin cache.
	var rbacUC *app.RBACUseCase
	if err := rdb.Ping(ctx).Err(); err != nil {
		logger.Warn("redis unavailable, running without policy cache", zap.Error(err))
		rdb.Close()
		rbacUC = app.NewRBACUseCase(roleRepo, permRepo, rolePermRepo, userRoleRepo, denialRepo, moduleGate, systemRoles, logger)
	} else {
		cachedRepo := redisadapter.NewCachedUserRoleRepo(userRoleRepo, rdb)
		rbacUC = app.NewRBACUseCase(roleRepo, permRepo, rolePermRepo, cachedRepo, denialRepo, moduleGate, systemRoles, logger)
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
