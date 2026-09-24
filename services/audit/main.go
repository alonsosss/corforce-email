package main

import (
	"context"
	"errors"
	"fmt"
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
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/audit/internal/adapters/cli"
	handler "github.com/alonsosss/corforce-email/services/audit/internal/adapters/http"
	"github.com/alonsosss/corforce-email/services/audit/internal/adapters/keyring"
	natsadapter "github.com/alonsosss/corforce-email/services/audit/internal/adapters/nats"
	outboxadapter "github.com/alonsosss/corforce-email/services/audit/internal/adapters/outbox"
	"github.com/alonsosss/corforce-email/services/audit/internal/adapters/postgres"
	metrics "github.com/alonsosss/corforce-email/services/audit/internal/adapters/prometheus"
	"github.com/alonsosss/corforce-email/services/audit/internal/adapters/sweep"
	"github.com/alonsosss/corforce-email/services/audit/internal/adapters/transactionalcli"
	"github.com/alonsosss/corforce-email/services/audit/internal/app"
	"github.com/alonsosss/corforce-email/services/audit/internal/ports"
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

	// AUDIT_ANCHOR_REPORT_INTERVAL: cada cuanto sale el informe de anclas a AUDIT_ANCHOR_RUA
	// (docs/adr/0006, seccion 8). Sin direcciones no hay informe.
	defaultAnchorReportInterval = 24 * time.Hour
	minAnchorReportInterval     = time.Hour
	maxAnchorReportInterval     = 7 * 24 * time.Hour
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == cli.VerifyCommand {
		os.Exit(cli.VerifyAnchors(os.Args[2:], os.Stdout, os.Stderr, openTenantChain))
	}
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
	anchorReport, err := loadAnchorReportSettings()
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
	anchorRepo := postgres.NewChainAnchorRepo(ctxPool)
	tenantDirectory := postgres.NewRegistryTenants(registryPool.Pool)

	// Ancla externa (docs/adr/0006, seccion 8): el informe de anclas sale como correo de la
	// plataforma a AUDIT_ANCHOR_RUA. Sin direcciones, nada cambia. Va antes del caso de uso porque
	// tambien recibe las cadenas que una verificacion da por rotas.
	var (
		anchorReporter *app.AnchorReporter
		chainBreaks    ports.ChainBreakNotifier
	)
	integrityMetrics.ReportSchedule(0)
	if len(anchorReport.recipients) > 0 {
		anchorReporter = app.NewAnchorReporter(app.AnchorReporterDeps{
			Anchors:    anchorRepo,
			Directory:  tenantDirectory,
			Sender:     transactionalcli.New(anchorReport.transactionalURL, anchorReport.internalToken, anchorReport.platformTenant),
			Signer:     keyring.NewSigner(hashKeys),
			Metrics:    integrityMetrics,
			Recipients: anchorReport.recipients,
			Logger:     logger,
		})
		chainBreaks = anchorReporter
		integrityMetrics.ReportSchedule(anchorReport.every)
		if hashKeys == nil {
			logger.Warn("audit: el informe de anclas saldra sin firma (sin AUDIT_HASH_KEY): quien lo reciba no podra comprobar que no se fabrico")
		}
	} else {
		logger.Info("audit: sin informe de anclas a una direccion externa (AUDIT_ANCHOR_RUA vacia); la cabeza anclada solo vive en este servidor (docs/adr/0006)")
	}

	uc := app.NewAuditUseCase(app.AuditDeps{
		Logs:         logRepo,
		Security:     secRepo,
		Changes:      changeRepo,
		Summary:      summaryRepo,
		Events:       eventPub,
		Logger:       logger,
		Anchors:      anchorRepo,
		AnchorEvents: outboxadapter.NewPublisher(ctxPool),
		Tx:           ctxPool,
		ChainBreaks:  chainBreaks,

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
	if anchorReporter != nil {
		go sweep.NewAnchorReport(registryPool.Pool, tenantDB, tenantDirectory, anchorReporter, logger, anchorReport.every).Run(ctx)
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
// buzones (quien las lanzo, sobre que buzon y desde que servidor), y el uso del
// asistente del webmail (quien, que accion y cuanto texto salio al proveedor, sin
// contenido; docs/adr/0015).
const defaultAuditSubjects = "identity.>,organization.>,access.>,gateway.>,scheduler.>,domains.>,migration.>,webmail.assistant.used"

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

// anchorReportSettings es la configuracion del informe de anclas. AUDIT_ANCHOR_RUA (direcciones de
// correo separadas por coma) lo activa; vacia, el servicio arranca igual y no envia nada. Con
// direcciones, hacen falta TRANSACTIONAL_URL, PLATFORM_TENANT_ID (la empresa de plataforma, como
// en identity: el remitente del sistema solo esta verificado en ella) y el token interno: un
// informe configurado que no puede salir es un error de configuracion, no un aviso.
type anchorReportSettings struct {
	recipients       []string
	every            time.Duration
	transactionalURL string
	internalToken    string
	platformTenant   uuid.UUID
}

func loadAnchorReportSettings() (anchorReportSettings, error) {
	var st anchorReportSettings
	seen := map[string]bool{}
	for _, raw := range strings.Split(os.Getenv("AUDIT_ANCHOR_RUA"), ",") {
		addr := strings.ToLower(strings.TrimSpace(raw))
		if addr == "" || seen[addr] {
			continue
		}
		v := validate.New()
		v.Email("AUDIT_ANCHOR_RUA", addr)
		if !v.Valid() {
			return st, fmt.Errorf("AUDIT_ANCHOR_RUA: %q no es una direccion de correo", addr)
		}
		seen[addr] = true
		st.recipients = append(st.recipients, addr)
	}
	var err error
	if st.every, err = config.EnvDuration("AUDIT_ANCHOR_REPORT_INTERVAL", defaultAnchorReportInterval, minAnchorReportInterval, maxAnchorReportInterval); err != nil {
		return st, err
	}
	if len(st.recipients) == 0 {
		return st, nil
	}
	if st.transactionalURL, err = config.ServiceURL("TRANSACTIONAL_URL", ""); err != nil {
		return st, err
	}
	if st.transactionalURL == "" {
		return st, errors.New("AUDIT_ANCHOR_RUA esta configurada y falta TRANSACTIONAL_URL")
	}
	if st.internalToken, err = middleware.InternalGatewayToken(); err != nil {
		return st, err
	}
	rawTenant := strings.TrimSpace(os.Getenv("PLATFORM_TENANT_ID"))
	if rawTenant == "" {
		return st, errors.New("AUDIT_ANCHOR_RUA esta configurada y falta PLATFORM_TENANT_ID (la empresa de plataforma con la que sale el correo del sistema)")
	}
	if st.platformTenant, err = uuid.Parse(rawTenant); err != nil {
		return st, fmt.Errorf("PLATFORM_TENANT_ID no es un uuid valido: %w", err)
	}
	return st, nil
}

// openTenantChain abre la base de empresa que se da a `audit verificar-ancla` con el adaptador de
// Postgres del servicio.
func openTenantChain(ctx context.Context, dsn string) (cli.ChainFactsReader, func(), error) {
	chain, err := postgres.OpenTenantChain(ctx, dsn)
	if err != nil {
		return nil, nil, err
	}
	return chain, chain.Close, nil
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
