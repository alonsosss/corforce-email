package main

import (
	"context"
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
	metrics "github.com/alonsosss/corforce-email/services/audit/internal/adapters/prometheus"
	"github.com/alonsosss/corforce-email/services/audit/internal/adapters/sweep"
	"github.com/alonsosss/corforce-email/services/audit/internal/app"
	"github.com/go-chi/chi/v5"
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

	// Verificacion en segundo plano (docs/adr/0006): hasta que tamano de cadena contesta GET
	// /integrity dentro de la peticion, cuanto puede durar una verificacion y cada cuanto una
	// verificacion periodica pasa de incremental a completa.
	minInlineMaxRows = 1_000
	maxInlineMaxRows = 50_000_000
	maxRunTimeout    = 72 * time.Hour
	minFullEvery     = time.Hour
	maxFullEvery     = 90 * 24 * time.Hour

	// AUDIT_INTEGRITY_SWEEP_INTERVAL: cada cuanto se verifica cada cadena sin que nadie lo pida.
	// Vacio o 0 lo desactiva.
	minSweepInterval = time.Hour
	maxSweepInterval = 7 * 24 * time.Hour
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
	inlineMaxRows, err := config.EnvInt("AUDIT_INTEGRITY_INLINE_MAX_ROWS", app.DefaultInlineMaxRows, minInlineMaxRows, maxInlineMaxRows)
	if err != nil {
		log.Fatal(err)
	}
	runTimeout, err := config.EnvDuration("AUDIT_INTEGRITY_RUN_TIMEOUT", app.DefaultRunTimeout, time.Minute, maxRunTimeout)
	if err != nil {
		log.Fatal(err)
	}
	fullEvery, err := config.EnvDuration("AUDIT_INTEGRITY_FULL_EVERY", app.DefaultFullEvery, minFullEvery, maxFullEvery)
	if err != nil {
		log.Fatal(err)
	}
	sweepEvery, err := integritySweepInterval()
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

	integrityMetrics := metrics.New()
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
		Integrity: app.IntegrityRunConfig{
			Runs:          postgres.NewIntegrityRunRepo(ctxPool),
			Metrics:       integrityMetrics,
			Background:    ctx,
			RunTimeout:    runTimeout,
			InlineMaxRows: int64(inlineMaxRows),
			FullEvery:     fullEvery,
		},
	})

	// El anuncio de cada ancla sale por la outbox: se encola con el ancla y el rele lo entrega.
	if err := bus.EnsureStream("AUDIT_CHAIN", []string{"audit.chain.>"}); err != nil {
		logger.Warn("ensure stream AUDIT_CHAIN", zap.Error(err))
	}
	go outbox.RunForTenants(ctx, tenantDB, db.PoolFromCtx, bus, logger, outbox.Options{})
	go sweep.New(registryPool.Pool, tenantDB, uc, logger, anchorEvery).Run(ctx)
	if sweepEvery > 0 {
		go sweep.NewIntegrity(registryPool.Pool, tenantDB, uc, integrityMetrics, logger, sweepEvery).Run(ctx)
	} else {
		logger.Info("audit: sin barrido periodico de verificacion de las cadenas (AUDIT_INTEGRITY_SWEEP_INTERVAL); solo se verifica bajo demanda")
	}

	h := handler.NewHandler(uc, perms)

	// Detector de seguridad: convierte el flujo de identidad (logins, fallos,
	// bloqueos, revocaciones) en eventos accionables de audit.security_events.
	detector := app.NewSecurityDetector(logRepo, secRepo, eventPub, app.SecurityDetectorConfig{
		BruteForceMax:    int64(bruteForceMax),
		BruteForceWindow: time.Duration(bruteForceWindowMin) * time.Minute,
	}, logger)

	// Eventos de dominio: consumidores durables de JetStream, uno por subject. Lo publicado con audit
	// caido queda en su stream y se aplica al volver; la caida de NATS no impide arrancar (reintentan).
	sources, err := natsadapter.SourcesFor(auditSubjects())
	if err != nil {
		log.Fatalf("AUDIT_SUBJECTS: %v", err)
	}
	eventConsumer := natsadapter.NewEventConsumer(bus, uc, detector, tenantDB, sources, logger)
	go eventConsumer.Run(ctx)
	defer eventConsumer.Stop()

	// Rastro de escrituras del API publicado por el gateway (JetStream durable).
	apiTrail := natsadapter.NewAPITrailWorker(bus, uc, tenantDB, logger)
	go apiTrail.Run(ctx)
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

// integritySweepInterval lee AUDIT_INTEGRITY_SWEEP_INTERVAL. Esta desactivado por defecto: verificar
// cada cadena es leerla entera de vez en cuando en una base compartida, y quien opera decide cada
// cuanto le compensa.
func integritySweepInterval() (time.Duration, error) {
	switch strings.TrimSpace(os.Getenv("AUDIT_INTEGRITY_SWEEP_INTERVAL")) {
	case "", "0", "0s":
		return 0, nil
	}
	return config.EnvDuration("AUDIT_INTEGRITY_SWEEP_INTERVAL", minSweepInterval, minSweepInterval, maxSweepInterval)
}
