package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
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
	handler "github.com/alonsosss/corforce-email/services/audit/internal/adapters/http"
	natsadapter "github.com/alonsosss/corforce-email/services/audit/internal/adapters/nats"
	outboxadapter "github.com/alonsosss/corforce-email/services/audit/internal/adapters/outbox"
	"github.com/alonsosss/corforce-email/services/audit/internal/adapters/postgres"
	"github.com/alonsosss/corforce-email/services/audit/internal/adapters/sweep"
	"github.com/alonsosss/corforce-email/services/audit/internal/app"
	"github.com/alonsosss/corforce-email/services/audit/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	defaultPort                = 8030
	defaultBruteForceMax       = 5
	defaultBruteForceWindowMin = 15
	// Fallos de inicio de sesion desde una IP, sumados los de todas las cuentas detras de ella
	// (una oficina con NAT), como el freno por IP de mail-auth.
	maxBruteForceMax = 10000
	// El recuento y la alerta unica por IP comparten la ventana: con mas de un dia, una errata
	// callaria las alertas repetidas de esa IP durante todo ese tiempo.
	maxBruteForceWindowMin = 24 * 60

	// AUDIT_ANCHOR_INTERVAL: cada cuanto se registra la cabeza de las cadenas. Es lo que puede
	// tardar en notarse el borrado de las ultimas filas escritas desde el ultimo ancla.
	defaultAnchorInterval = 15 * time.Minute
	minAnchorInterval     = time.Minute
	maxAnchorInterval     = 24 * time.Hour

	// Plazo de cada verificacion de cadena (AUDIT_VERIFY_TIMEOUT), por debajo del WriteTimeout de
	// pkg/server (30 s): pasado ese, la respuesta ya no llegaria.
	maxVerifyTimeout = 28 * time.Second

	// eventHandlerTimeout acota el trabajo de audit por cada evento del bus.
	eventHandlerTimeout = 30 * time.Second
)

func main() {
	logger, _ := zap.NewProduction()
	defer logger.Sync()
	response.SetUnexpectedLogger(logger)

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	port, err := config.EnvInt("AUDIT_PORT", defaultPort, 1, config.MaxPort)
	if err != nil {
		log.Fatal(err)
	}
	bruteForceMax, err := config.EnvInt("SECURITY_BRUTEFORCE_MAX", defaultBruteForceMax, 1, maxBruteForceMax)
	if err != nil {
		log.Fatal(err)
	}
	bruteForceWindowMin, err := config.EnvInt("SECURITY_BRUTEFORCE_WINDOW_MIN", defaultBruteForceWindowMin, 1, maxBruteForceWindowMin)
	if err != nil {
		log.Fatal(err)
	}
	anchorEvery, err := config.EnvDuration("AUDIT_ANCHOR_INTERVAL", defaultAnchorInterval, minAnchorInterval, maxAnchorInterval)
	if err != nil {
		log.Fatal(err)
	}
	verifyTimeout, err := config.EnvDuration("AUDIT_VERIFY_TIMEOUT", app.DefaultVerifyTimeout, time.Second, maxVerifyTimeout)
	if err != nil {
		log.Fatal(err)
	}
	perms, err := authz.CheckerFromEnv()
	if err != nil {
		log.Fatal(err)
	}
	hashKeys, err := crypto.LoadMACKeyRing("AUDIT_HASH_KEY", "AUDIT_HASH_KEYS_OLD")
	if err != nil {
		log.Fatalf("audit: clave de la cadena de hash: %v", err)
	}
	if hashKeys == nil {
		logger.Warn("audit: sin AUDIT_HASH_KEY: las filas nuevas se firman con el hash sin clave (version 1) y los eventos de seguridad no se encadenan; quien escriba en la base puede recalcular la cadena (docs/adr/0006)")
	} else {
		logger.Info("audit: cadena de hash con clave (version 2)",
			zap.String("key_id", hashKeys.ActiveID()), zap.Bool("retired_keys", hashKeys.HasOldKeys()))
	}

	// ctx gobierna los trabajos de fondo (rele de la outbox, anclaje de cadenas): se cancela
	// cuando el HTTP termina de apagarse.
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

	bus, err := events.NewBus(cfg.NATS.URL, logger)
	if err != nil {
		log.Fatalf("connect nats: %v", err)
	}
	defer bus.Close()

	logRepo := postgres.NewAuditLogRepo(ctxPool, hashKeys)
	secRepo := postgres.NewSecurityEventRepo(ctxPool, hashKeys)
	changeRepo := postgres.NewDataChangeRepo(ctxPool)
	summaryRepo := postgres.NewAuditSummaryRepo(ctxPool)
	eventPub := natsadapter.NewEventPublisher(bus)

	uc := app.NewAuditUseCase(app.AuditDeps{
		Logs:         logRepo,
		Security:     secRepo,
		Changes:      changeRepo,
		Summary:      summaryRepo,
		Events:       eventPub,
		Logger:       logger,
		Anchors:      postgres.NewChainAnchorRepo(ctxPool),
		AnchorEvents: outboxadapter.NewPublisher(ctxPool),
		Tx:           ctxPool,

		VerifyTimeout: verifyTimeout,
	})

	// El anuncio de cada ancla sale por la outbox: se encola con el ancla y el rele lo entrega.
	if err := bus.EnsureStream("AUDIT_CHAIN", []string{"audit.chain.>"}); err != nil {
		logger.Warn("ensure stream AUDIT_CHAIN", zap.Error(err))
	}
	go outbox.RunForTenants(ctx, tenantDB, db.PoolFromCtx, bus, logger, outbox.Options{})
	go sweep.New(registryPool.Pool, tenantDB, uc, logger, anchorEvery).Run(ctx)

	h := handler.NewHandler(uc, perms)

	// Detector de seguridad: convierte el flujo de identidad (logins, fallos,
	// bloqueos, revocaciones) en eventos accionables de audit.security_events.
	detector := app.NewSecurityDetector(logRepo, secRepo, eventPub, app.SecurityDetectorConfig{
		BruteForceMax:    int64(bruteForceMax),
		BruteForceWindow: time.Duration(bruteForceWindowMin) * time.Minute,
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

	srv := server.New(port, r, logger)
	runErr := srv.Run()
	// El rele de la outbox y el anclaje retienen conexiones del pool que los cierres diferidos esperan:
	// se cancelan antes de cerrarlo.
	cancel()
	if runErr != nil {
		logger.Fatal("server error", zap.Error(runErr))
	}
}

func subscribeToEvents(bus *events.Bus, uc *app.AuditUseCase, detector *app.SecurityDetector, tenantDB *db.TenantDB, logger *zap.Logger) {
	handle := func(evt events.Event) {
		userID, _ := uuid.Parse(evt.UserID)
		tenantID, _ := uuid.Parse(evt.TenantID)
		if tenantID == uuid.Nil {
			return
		}

		// Las suscripciones de nucleo entregan de una en una: un evento que se cuelga (base lenta, cerrojo
		// de la cadena) retiene a todos los que vienen detras.
		base, cancel := context.WithTimeout(context.Background(), eventHandlerTimeout)
		defer cancel()
		pool, err := tenantDB.ResolveForTenant(base, tenantID.String())
		if err != nil {
			logger.Warn("audit: resolve tenant pool", zap.String("tenant", tenantID.String()), zap.Error(err))
			return
		}
		ctx := db.WithPool(base, pool)

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
// auditoria: identidad, organizacion, accesos, el gateway, los trabajos
// programados, los dominios de correo (alta, verificacion y claves DKIM: una
// revocacion por clave comprometida lleva motivo y actor) y las migraciones de
// buzones (quien las lanzo, sobre que buzon y desde que servidor).
const defaultAuditSubjects = "identity.>,organization.>,access.>,gateway.>,scheduler.>,domains.>,migration.>"

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
