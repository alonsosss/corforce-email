package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/config"
	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/server"
	handler "github.com/alonsosss/corforce-email/services/audit/internal/adapters/http"
	natsadapter "github.com/alonsosss/corforce-email/services/audit/internal/adapters/nats"
	"github.com/alonsosss/corforce-email/services/audit/internal/adapters/postgres"
	"github.com/alonsosss/corforce-email/services/audit/internal/app"
	"github.com/alonsosss/corforce-email/services/audit/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
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

	registryPool, err := db.NewPool(ctx, cfg.Postgres.DSN(), logger)
	if err != nil {
		log.Fatalf("connect db: %v", err)
	}
	defer registryPool.Close()

	tenantDB, mgr := db.NewTenantRouting(registryPool.Pool, cfg.Postgres, logger)
	defer mgr.CloseAll()
	ctxPool := &db.ContextPool{}

	bus, err := events.NewBus(cfg.NATS.URL, logger)
	if err != nil {
		log.Fatalf("connect nats: %v", err)
	}
	defer bus.Close()

	logRepo := postgres.NewAuditLogRepo(ctxPool)
	secRepo := postgres.NewSecurityEventRepo(ctxPool)
	changeRepo := postgres.NewDataChangeRepo(ctxPool)
	summaryRepo := postgres.NewAuditSummaryRepo(ctxPool)
	eventPub := natsadapter.NewEventPublisher(bus)

	uc := app.NewAuditUseCase(app.AuditDeps{
		Logs:     logRepo,
		Security: secRepo,
		Changes:  changeRepo,
		Summary:  summaryRepo,
		Events:   eventPub,
		Logger:   logger,
	})

	h := handler.NewHandler(uc, authz.NewCheckerFromEnv())

	// Detector de seguridad: convierte el flujo de identidad (logins, fallos,
	// bloqueos, revocaciones) en eventos accionables de audit.security_events.
	detector := app.NewSecurityDetector(logRepo, secRepo, eventPub, app.SecurityDetectorConfig{
		BruteForceMax:    envInt64("SECURITY_BRUTEFORCE_MAX", 5),
		BruteForceWindow: time.Duration(envInt64("SECURITY_BRUTEFORCE_WINDOW_MIN", 15)) * time.Minute,
	}, logger)

	subscribeToEvents(bus, uc, detector, tenantDB, logger)

	// Rastro de escrituras del API publicado por el gateway (JetStream durable).
	apiTrail := natsadapter.NewAPITrailWorker(bus, uc, tenantDB, logger)
	if err := apiTrail.Start(); err != nil {
		logger.Error("api trail worker start failed", zap.Error(err))
	}
	defer apiTrail.Stop()

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RequireGatewayToken)
	r.Use(middleware.InjectFromGateway)
	r.Use(db.TenantPoolMiddleware(tenantDB))
	r.Use(middleware.SecureHeaders)
	r.Use(middleware.Logger(logger))
	limiter := middleware.NewRateLimiter(60, time.Minute)
	r.Use(limiter.Limit)
	r.Mount("/api/v1/audit", h.Routes())

	port := 8030
	if p := os.Getenv("AUDIT_PORT"); p != "" {
		if v, err := strconv.Atoi(p); err == nil {
			port = v
		}
	}

	srv := server.New(port, r, logger)
	if err := srv.Run(); err != nil {
		logger.Fatal("server error", zap.Error(err))
	}
}

func envInt64(key string, def int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func subscribeToEvents(bus *events.Bus, uc *app.AuditUseCase, detector *app.SecurityDetector, tenantDB *db.TenantDB, logger *zap.Logger) {
	handle := func(evt events.Event) {
		userID, _ := uuid.Parse(evt.UserID)
		tenantID, _ := uuid.Parse(evt.TenantID)
		if tenantID == uuid.Nil {
			return
		}

		pool, err := tenantDB.ResolveForTenant(context.Background(), tenantID.String())
		if err != nil {
			logger.Warn("audit: resolve tenant pool", zap.String("tenant", tenantID.String()), zap.Error(err))
			return
		}
		ctx := db.WithPool(context.Background(), pool)

		detail := ""
		// La IP real y el user-agent viajan en el payload del evento cuando el emisor
		// los conoce (p.ej. identity en user.logged_in); 0.0.0.0 solo como ausencia.
		ip := "0.0.0.0"
		userAgent := ""
		if data, ok := evt.Data.(map[string]interface{}); ok {
			if b, err := json.Marshal(data); err == nil {
				detail = string(b)
			}
			if v, ok := data["ip"].(string); ok && v != "" {
				ip = v
			}
			if v, ok := data["user_agent"].(string); ok {
				userAgent = v
			}
		}

		l := &domain.AuditLog{
			TenantID:  tenantID,
			UserID:    userID,
			Action:    evt.Type,
			Module:    evt.Source,
			Resource:  evt.Type,
			IPAddress: ip,
			Severity:  "info",
		}
		if userAgent != "" {
			l.UserAgent = &userAgent
		}
		if detail != "" {
			l.Changes = &detail
		}

		if err := uc.LogAction(ctx, l); err != nil {
			logger.Error("event audit log failed", zap.Error(err), zap.String("type", evt.Type))
			return
		}
		detector.Inspect(ctx, l, userAgent)
	}

	for _, subj := range auditSubjects() {
		if _, err := bus.QueueSubscribe(subj, "audit-service", handle); err != nil {
			logger.Error("subscribe failed", zap.String("subject", subj), zap.Error(err))
		}
	}
}

// defaultAuditSubjects son los dominios que hoy emiten eventos con valor de
// auditoria: identidad, organizacion, accesos, el gateway y los trabajos
// programados.
const defaultAuditSubjects = "identity.>,organization.>,access.>,gateway.>,scheduler.>"

// auditSubjects lee de AUDIT_SUBJECTS (lista separada por comas) los subjects que
// se vuelcan en la bitacora. Va por configuracion y no en codigo porque la
// plataforma incorpora dominios nuevos (dominios de correo, cuentas, campanas)
// sin que este servicio cambie: auditar uno mas es anadir su prefijo al entorno,
// y excluir un flujo demasiado ruidoso para la bitacora es quitarlo, sin
// redesplegar audit.
func auditSubjects() []string {
	raw := os.Getenv("AUDIT_SUBJECTS")
	if strings.TrimSpace(raw) == "" {
		raw = defaultAuditSubjects
	}
	var subjects []string
	for _, s := range strings.Split(raw, ",") {
		if s = strings.TrimSpace(s); s != "" {
			subjects = append(subjects, s)
		}
	}
	return subjects
}
