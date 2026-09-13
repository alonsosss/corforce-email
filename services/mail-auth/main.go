// mail-auth verifica las contrasenas de buzon que Dovecot le consulta por HTTPS desde
// passwd-verify.lua (IMAP, POP3, ManageSieve, SMTP via SASL de Dovecot y el webmail).
//
// Dos listeners:
//   - TLS (MAIL_AUTH_TLS_PORT, 9082): POST / y POST /auth para Dovecot. Sin token de
//     gateway, solo alcanzable desde la red de los motores.
//   - HTTP (MAIL_AUTH_PORT, 8041): /healthz, /metrics y el endpoint interno de
//     consulta de inicios, tras RequireGatewayToken.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/alonsosss/corforce-email/pkg/config"
	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/observability"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/server"
	bcryptadapter "github.com/alonsosss/corforce-email/services/mail-auth/internal/adapters/bcrypt"
	handler "github.com/alonsosss/corforce-email/services/mail-auth/internal/adapters/http"
	"github.com/alonsosss/corforce-email/services/mail-auth/internal/adapters/postgres"
	promadapter "github.com/alonsosss/corforce-email/services/mail-auth/internal/adapters/prometheus"
	redisadapter "github.com/alonsosss/corforce-email/services/mail-auth/internal/adapters/redis"
	"github.com/alonsosss/corforce-email/services/mail-auth/internal/app"
	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

const (
	defaultPort    = 8041
	defaultTLSPort = 9082

	defaultMaxFailures      = 10
	defaultMaxFailuresPerIP = 50
	defaultFailureWindow    = 15 * time.Minute
	defaultLockTTL          = 30 * time.Minute

	// tlsHostname es el alias con el que Dovecot resuelve al servicio (MAIL_AUTH_URL).
	tlsHostname = "mail-auth"
)

func main() {
	logger, _ := zap.NewProduction()
	response.SetUnexpectedLogger(logger)
	defer logger.Sync()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	ctx := context.Background()
	dsn, err := cfg.Postgres.CellDSN()
	if err != nil {
		log.Fatalf("cell dsn: %v", err)
	}
	pool, err := db.NewNamedPool(ctx, dsn, "cell", logger)
	if err != nil {
		log.Fatalf("connect cell db: %v", err)
	}
	defer pool.Close()

	verifier, err := bcryptadapter.New()
	if err != nil {
		log.Fatalf("password verifier: %v", err)
	}

	uc := app.New(app.Deps{
		Repo:      postgres.NewRepository(&db.ContextPool{}),
		Passwords: verifier,
		Throttle:  newThrottle(ctx, cfg.Redis, logger),
		Metrics:   promadapter.New(),
		Logger:    logger,
	})
	h := handler.NewHandler(uc)

	// Listener TLS de Dovecot. Sin limitador por IP: el unico cliente es Dovecot y
	// acotarlo estrangularia a toda la celda; el freno va por real_rip en Redis.
	verifyRouter := chi.NewRouter()
	verifyRouter.Use(middleware.RequestID)
	verifyRouter.Use(db.StaticPoolMiddleware(pool.Pool))
	verifyRouter.Use(middleware.Logger(logger))
	verifyRouter.Mount("/", h.VerifyRoutes())

	tlsCfg, selfSigned, err := handler.TLSConfig(os.Getenv("MAIL_AUTH_TLS_CERT"), os.Getenv("MAIL_AUTH_TLS_KEY"), tlsHostname)
	if err != nil {
		log.Fatalf("tls: %v", err)
	}
	if selfSigned {
		logger.Warn("mail-auth: sin MAIL_AUTH_TLS_CERT/MAIL_AUTH_TLS_KEY, se usa un certificado autofirmado en memoria (Dovecot conecta sin verificarlo)")
	}
	tlsSrv := &http.Server{
		Addr:              fmt.Sprintf(":%d", envInt("MAIL_AUTH_TLS_PORT", defaultTLSPort)),
		Handler:           observability.HTTPMetrics()(verifyRouter),
		TLSConfig:         tlsCfg,
		ReadTimeout:       15 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	tlsErr := make(chan error, 1)
	go func() {
		logger.Info("mail-auth: listener TLS de Dovecot", zap.String("addr", tlsSrv.Addr))
		if err := tlsSrv.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			tlsErr <- err
		}
	}()

	// Listener HTTP interno: operativa (pkg/server pone /healthz y /metrics) y la consulta
	// de inicios que hace el plano de control con la empresa en cabecera.
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RequireGatewayToken)
	r.Use(middleware.InjectFromGateway)
	r.Use(db.StaticPoolMiddleware(pool.Pool))
	r.Use(middleware.SecureHeaders)
	r.Use(middleware.Logger(logger))
	r.Use(middleware.NewRateLimiter(120, time.Minute).Limit)
	r.Mount("/", h.InternalRoutes())

	srv := server.New(envInt("MAIL_AUTH_PORT", defaultPort), r, logger)
	runErr := make(chan error, 1)
	go func() { runErr <- srv.Run() }()

	select {
	case err := <-tlsErr:
		logger.Fatal("mail-auth: listener TLS", zap.Error(err))
	case err := <-runErr:
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if serr := tlsSrv.Shutdown(shutdownCtx); serr != nil {
			logger.Error("mail-auth: cierre del listener TLS", zap.Error(serr))
		}
		if err != nil {
			logger.Fatal("server error", zap.Error(err))
		}
	}
}

// newThrottle abre el freno de fuerza bruta sobre el Redis de la plataforma. Sin Redis
// el servicio arranca sin freno y lo avisa: la capa de red la sigue dando netfilter.
func newThrottle(ctx context.Context, rc config.RedisConfig, logger *zap.Logger) *redisadapter.Throttle {
	rdb := redis.NewClient(&redis.Options{Addr: rc.Addr(), Password: rc.Password, DB: 0})
	if err := rdb.Ping(ctx).Err(); err != nil {
		logger.Warn("mail-auth: Redis no disponible, se arranca SIN freno de fuerza bruta", zap.Error(err))
		_ = rdb.Close()
		return nil
	}
	tcfg := redisadapter.Config{
		MaxFailures:      int64(envInt("MAIL_AUTH_MAX_FAILURES", defaultMaxFailures)),
		MaxFailuresPerIP: int64(envInt("MAIL_AUTH_MAX_FAILURES_PER_IP", defaultMaxFailuresPerIP)),
		Window:           envDuration("MAIL_AUTH_FAILURE_WINDOW", defaultFailureWindow),
		LockTTL:          envDuration("MAIL_AUTH_LOCK_TTL", defaultLockTTL),
	}
	logger.Info("mail-auth: freno de fuerza bruta activo",
		zap.Int64("max_failures", tcfg.MaxFailures), zap.Int64("max_failures_per_ip", tcfg.MaxFailuresPerIP),
		zap.Duration("window", tcfg.Window), zap.Duration("lock_ttl", tcfg.LockTTL))
	return redisadapter.NewThrottle(rdb, tcfg, logger)
}

func envInt(key string, fallback int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil {
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
