package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/config"
	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/objectstore"
	"github.com/alonsosss/corforce-email/pkg/outbox"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/server"
	clamavadapter "github.com/alonsosss/corforce-email/services/templates/internal/adapters/clamav"
	handler "github.com/alonsosss/corforce-email/services/templates/internal/adapters/http"
	objectstoreadapter "github.com/alonsosss/corforce-email/services/templates/internal/adapters/objectstore"
	outboxadapter "github.com/alonsosss/corforce-email/services/templates/internal/adapters/outbox"
	"github.com/alonsosss/corforce-email/services/templates/internal/adapters/postgres"
	"github.com/alonsosss/corforce-email/services/templates/internal/adapters/spamcheck"
	"github.com/alonsosss/corforce-email/services/templates/internal/adapters/transactionalcli"
	"github.com/alonsosss/corforce-email/services/templates/internal/app"
	"github.com/alonsosss/corforce-email/services/templates/internal/ports"
	"github.com/alonsosss/corforce-email/services/templates/internal/render"
	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

const (
	defaultPort = 8047
	// clamdTimeout acota el analisis de una imagen (hasta 5 MiB).
	clamdTimeout       = 30 * time.Second
	bucketCheckTimeout = 5 * time.Second
)

func main() {
	logger, _ := zap.NewProduction()
	defer logger.Sync()
	response.SetUnexpectedLogger(logger)

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	port, err := config.EnvInt("TEMPLATES_PORT", defaultPort, 1, config.MaxPort)
	if err != nil {
		log.Fatal(err)
	}
	perms, err := authz.CheckerFromEnv()
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	registryPool, err := db.NewPool(ctx, cfg.Postgres.DSN(), logger)
	if err != nil {
		log.Fatalf("connect db: %v", err)
	}
	defer registryPool.Close()

	// Enrutado por celda: el pool de cada empresa se abre contra el host de su celda.
	tenantDB, mgr := db.NewTenantRouting(registryPool.Pool, cfg.Postgres, logger)
	defer mgr.CloseAll()
	ctxPool := &db.ContextPool{}

	// Los eventos salen por la outbox de cada base de empresa; sin NATS el servicio
	// arranca igual y los eventos quedan encolados hasta que el rele pueda entregarlos.
	if bus, err := events.NewBus(cfg.NATS.URL, logger); err != nil {
		logger.Warn("templates: NATS no disponible, los eventos quedan en la outbox", zap.Error(err))
	} else {
		defer bus.Close()
		if err := bus.EnsureStream(outboxadapter.StreamName, []string{outboxadapter.StreamSubjects}); err != nil {
			logger.Warn("ensure stream "+outboxadapter.StreamName, zap.Error(err))
		}
		go outbox.RunForTenants(ctx, tenantDB, db.PoolFromCtx, bus, logger, outbox.Options{})
	}

	editor, err := loadEditorDeps(ctx, logger)
	if err != nil {
		log.Fatalf("templates: %v", err)
	}

	repo := postgres.NewRepository(ctxPool)
	uc := app.New(app.Deps{
		Repo:       repo,
		Tx:         ctxPool,
		Renderer:   render.NewPort(),
		Events:     outboxadapter.NewPublisher(ctxPool),
		BrandKits:  repo,
		Assets:     repo,
		Store:      editor.store,
		Scanner:    editor.scanner,
		Spam:       editor.spam,
		TestSender: editor.testSender,
		Logger:     logger,
	})
	h := handler.NewHandler(uc, perms)

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RequireGatewayToken)
	r.Use(middleware.SecureHeaders)
	r.Use(middleware.Logger(logger))

	// API publico: identidad y empresa del gateway, permiso por accion en el handler y
	// cupo por usuario (todo el trafico comparte la IP del gateway).
	r.Group(func(r chi.Router) {
		r.Use(middleware.InjectFromGateway)
		r.Use(db.TenantPoolMiddleware(tenantDB))
		r.Use(middleware.NewRateLimiter(120, time.Minute).LimitPerUser)
		r.Mount("/api/v1/templates", h.Routes())
	})

	// API interno para transactional y campaigns: un renderizado por envio, sin cupo. La
	// empresa viene en X-Tenant-ID y solo se acepta tras el token interno.
	r.Group(func(r chi.Router) {
		r.Use(middleware.InjectFromGateway)
		r.Use(db.TenantHeaderPoolMiddleware(tenantDB))
		r.Mount("/internal/templates", h.InternalRoutes())
	})

	srv := server.New(port, r, logger)
	if err := srv.Run(); err != nil {
		logger.Fatal("server error", zap.Error(err))
	}
}

// editorDeps son las dependencias opcionales del editor visual (docs/Plan_Editor_Correos.md).
// Cada una ausente degrada su funcion y el servicio arranca: sin almacen o sin ClamAV las
// subidas responden 503, sin mail-security la verificacion sale sin puntuacion antispam y sin
// transactional el envio de prueba responde 503. Una mal configurada impide arrancar.
type editorDeps struct {
	store      ports.AssetStore
	scanner    ports.VirusScanner
	spam       ports.SpamChecker
	testSender ports.TestSender
}

func loadEditorDeps(ctx context.Context, logger *zap.Logger) (editorDeps, error) {
	var deps editorDeps

	objects, err := objectstore.FromEnv()
	if err != nil {
		return deps, err
	}
	if objects == nil {
		logger.Warn("templates: sin MINIO_ENDPOINT, las subidas de imagenes responden STORAGE_UNAVAILABLE")
	} else {
		store, err := objectstoreadapter.New(objects, os.Getenv("PUBLIC_BASE_URL"))
		if err != nil {
			return deps, err
		}
		verifyCtx, cancel := context.WithTimeout(ctx, bucketCheckTimeout)
		if err := objects.Verify(verifyCtx); err != nil {
			logger.Warn("templates: el bucket de imagenes no responde; las subidas fallaran hasta que lo haga", zap.Error(err))
		}
		cancel()
		deps.store = store
	}

	if addr := strings.TrimSpace(os.Getenv("TEMPLATES_CLAMD_ADDR")); addr == "" {
		logger.Warn("templates: sin TEMPLATES_CLAMD_ADDR, las subidas de imagenes responden SCANNER_UNAVAILABLE")
	} else {
		host, port, err := net.SplitHostPort(addr)
		if err != nil || !config.ValidHost(host) {
			return deps, fmt.Errorf("TEMPLATES_CLAMD_ADDR=%q debe ser host:puerto", addr)
		}
		if _, err := config.ParsePort(port); err != nil {
			return deps, fmt.Errorf("TEMPLATES_CLAMD_ADDR: %w", err)
		}
		deps.scanner = clamavadapter.New(addr, clamdTimeout)
	}

	spamURL, err := config.ServiceURL("SPAM_CHECK_URL", "")
	if err != nil {
		return deps, err
	}
	from := strings.TrimSpace(os.Getenv("PLATFORM_FROM_EMAIL"))
	switch {
	case spamURL == "":
		logger.Warn("templates: sin SPAM_CHECK_URL, la verificacion sale sin puntuacion antispam")
	case from == "":
		logger.Warn("templates: sin PLATFORM_FROM_EMAIL no hay remitente para el mensaje de prueba; la verificacion sale sin puntuacion antispam")
	default:
		token, err := middleware.InternalGatewayToken()
		if err != nil {
			return deps, err
		}
		spam, err := spamcheck.New(spamURL, token, from)
		if err != nil {
			return deps, err
		}
		deps.spam = spam
	}

	// El envio de prueba sale por transactional, el unico que habla con SES.
	transactionalURL, err := config.ServiceURL("TRANSACTIONAL_URL", "")
	if err != nil {
		return deps, err
	}
	if transactionalURL == "" {
		logger.Warn("templates: sin TRANSACTIONAL_URL, el envio de prueba responde TEST_SEND_UNAVAILABLE")
	} else {
		token, err := middleware.InternalGatewayToken()
		if err != nil {
			return deps, err
		}
		deps.testSender = transactionalcli.New(transactionalURL, token)
	}
	return deps, nil
}
