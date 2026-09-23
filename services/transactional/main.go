package main

import (
	"context"
	"log"
	"os"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/config"
	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/outbox"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/server"
	handler "github.com/alonsosss/corforce-email/services/transactional/internal/adapters/http"
	natsadapter "github.com/alonsosss/corforce-email/services/transactional/internal/adapters/nats"
	"github.com/alonsosss/corforce-email/services/transactional/internal/adapters/postgres"
	promadapter "github.com/alonsosss/corforce-email/services/transactional/internal/adapters/prometheus"
	"github.com/alonsosss/corforce-email/services/transactional/internal/adapters/reputationclient"
	"github.com/alonsosss/corforce-email/services/transactional/internal/adapters/sesclient"
	"github.com/alonsosss/corforce-email/services/transactional/internal/adapters/sns"
	"github.com/alonsosss/corforce-email/services/transactional/internal/adapters/suppressionclient"
	"github.com/alonsosss/corforce-email/services/transactional/internal/adapters/templatesclient"
	"github.com/alonsosss/corforce-email/services/transactional/internal/app"
	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	defaultPort                   = 8045
	defaultWorkers                = 4
	defaultSendRate               = 10.0
	defaultMarketingWorkers       = 4
	defaultMarketingSendRate      = 10.0
	defaultAccountMonitorInterval = 5 * time.Minute
	// maxWorkers acota cada carril: un trabajador ocupa una conexion del pool de la empresa
	// (10, pkg/db) mientras envia y la tasa ya la fija SES_MAX_SEND_RATE; mas solo esperan.
	maxWorkers = 64
	// Tasa de cada carril, envios por segundo a SES: desde 1, la de una cuenta en sandbox. El
	// techo solo detiene una errata; la cuota real la fija SES para cada cuenta.
	minSendRate = 1.0
	maxSendRate = 10000.0
	// releaseInterval es la cadencia con la que se encolan los programados vencidos.
	releaseInterval = time.Minute
)

func main() {
	logger, _ := zap.NewProduction()
	defer logger.Sync()
	response.SetUnexpectedLogger(logger)

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	workers, err := config.EnvInt("TRANSACTIONAL_WORKERS", defaultWorkers, 1, maxWorkers)
	if err != nil {
		log.Fatal(err)
	}
	marketingWorkers, err := config.EnvInt("TRANSACTIONAL_MARKETING_WORKERS", defaultMarketingWorkers, 1, maxWorkers)
	if err != nil {
		log.Fatal(err)
	}
	port, err := config.EnvInt("TRANSACTIONAL_PORT", defaultPort, 1, config.MaxPort)
	if err != nil {
		log.Fatal(err)
	}
	sendRate, err := config.EnvFloat("SES_MAX_SEND_RATE", defaultSendRate, minSendRate, maxSendRate)
	if err != nil {
		log.Fatal(err)
	}
	marketingRate, err := config.EnvFloat("SES_MAX_SEND_RATE_MARKETING", defaultMarketingSendRate, minSendRate, maxSendRate)
	if err != nil {
		log.Fatal(err)
	}
	// Vigilante del estado de la cuenta de SES (cuota, pausa, reputacion). "0" lo apaga: un
	// entorno sin SES no debe disparar la alerta de datos viejos.
	var accountInterval time.Duration
	if strings.TrimSpace(os.Getenv("SES_ACCOUNT_MONITOR_INTERVAL")) != "0" {
		if accountInterval, err = config.EnvDuration("SES_ACCOUNT_MONITOR_INTERVAL", defaultAccountMonitorInterval, time.Minute, time.Hour); err != nil {
			log.Fatal(err)
		}
	}
	// Sin suppression no se encola ningun envio (toda exclusion se respeta antes de encolar), asi
	// que su URL es obligatoria. Templates y reputation son opcionales: sin ellas fallan o se
	// degradan solo los envios que dependen de ellas.
	suppressionURL, err := config.RequiredServiceURL("SUPPRESSION_URL")
	if err != nil {
		log.Fatal(err)
	}
	templatesURL, err := config.ServiceURL("TEMPLATES_URL", "")
	if err != nil {
		log.Fatal(err)
	}
	reputationURL, err := config.ServiceURL("REPUTATION_URL", "")
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

	tenantDB, mgr := db.NewTenantRouting(registryPool.Pool, cfg.Postgres, logger)
	defer mgr.CloseAll()
	ctxPool := &db.ContextPool{}

	// Los enlaces de baja se firman con MAIL_LINK_SIGNING_KEY y cuelgan de PUBLIC_BASE_URL
	// (la ruta publica la sirve el gateway bajo /api/v1/public/transactional).
	linkBase := strings.TrimSpace(os.Getenv("PUBLIC_BASE_URL"))
	if linkBase == "" {
		log.Fatal("PUBLIC_BASE_URL es obligatoria para construir los enlaces de baja")
	}
	links, err := domain.NewLinkSigner(os.Getenv("MAIL_LINK_SIGNING_KEY"), linkBase)
	if err != nil {
		log.Fatalf("MAIL_LINK_SIGNING_KEY: %v", err)
	}

	sesOpts := sesclient.OptionsFromEnv()
	sender, err := sesclient.New(ctx, sesOpts)
	if err != nil {
		log.Fatalf("configurar SES: %v", err)
	}
	// El marketing sale por su propio configuration set: sin el, o con el mismo que el
	// transaccional, las dos clases compartirian reputacion y eventos en SES.
	marketingSet := strings.TrimSpace(os.Getenv("SES_CONFIG_SET_MARKETING"))
	if marketingSet == "" || marketingSet == strings.TrimSpace(sesOpts.ConfigurationSet) {
		log.Fatal("SES_CONFIG_SET_MARKETING es obligatoria y distinta de SES_CONFIG_SET_TRANSACTIONAL")
	}

	internalToken := os.Getenv("INTERNAL_GATEWAY_TOKEN")
	if reputationURL == "" {
		logger.Error("transactional: REPUTATION_URL no configurada; el marketing respondera 503 y el transaccional saldra sin autorizacion previa")
	}
	uc := app.New(app.Deps{
		Repo:        postgres.NewRepository(ctxPool),
		Events:      postgres.NewOutboxPublisher(ctxPool),
		Suppression: suppressionclient.New(suppressionURL, internalToken),
		Templates:   templatesclient.New(templatesURL, internalToken),
		Reputation:  reputationclient.New(reputationURL, internalToken),
		Sender:      sender,
		Limiter:     natsadapter.NewTokenBucket(sendRate, int(sendRate)),
		Marketing: app.Lane{
			Sender:  sender.WithConfigurationSet(marketingSet),
			Limiter: natsadapter.NewTokenBucket(marketingRate, int(marketingRate)),
		},
		Links: links,
		Config: app.Config{
			PlatformFromEmail:           os.Getenv("PLATFORM_FROM_EMAIL"),
			PlatformFromName:            os.Getenv("PLATFORM_FROM_NAME"),
			AllowUnverifiedPlatformFrom: strings.EqualFold(os.Getenv("PLATFORM_FROM_ALLOW_UNVERIFIED"), "true"),
		},
		Logger:  logger,
		Metrics: promadapter.New(),
	})
	if accountInterval > 0 {
		accountReader, err := sesclient.NewAccountReader(ctx, sesOpts)
		if err != nil {
			log.Fatalf("configurar el vigilante de SES: %v", err)
		}
		go uc.RunSESAccountMonitor(ctx, accountReader, accountInterval)
	}

	// Sin NATS el API sigue aceptando mensajes: quedan en la outbox y en la cola de la
	// base hasta que el bus vuelva. Lo que no hay es worker de envio ni proyeccion.
	if bus, err := events.NewBus(cfg.NATS.URL, logger); err != nil {
		logger.Error("transactional: NATS no disponible; sin worker de envio ni rele de outbox", zap.Error(err))
	} else {
		defer bus.Close()
		if err := natsadapter.EnsureStreams(bus); err != nil {
			logger.Error("transactional: no se pudieron asegurar los streams", zap.Error(err))
		}
		senderWorker := natsadapter.NewSenderWorker(bus, uc, tenantDB, workers, logger)
		if err := senderWorker.Start(); err != nil {
			logger.Error("transactional: el worker de envio no arranco", zap.Error(err))
		} else {
			defer senderWorker.Stop()
		}
		marketingWorker := natsadapter.NewMarketingSenderWorker(bus, uc, tenantDB, marketingWorkers, logger)
		if err := marketingWorker.Start(); err != nil {
			logger.Error("transactional: el worker de marketing no arranco", zap.Error(err))
		} else {
			defer marketingWorker.Stop()
		}
		domainsConsumer := natsadapter.NewDomainsConsumer(bus, uc, tenantDB, logger)
		if err := domainsConsumer.Start(); err != nil {
			logger.Error("transactional: el consumidor de dominios no arranco", zap.Error(err))
		} else {
			defer domainsConsumer.Stop()
		}
		go outbox.RunForTenants(ctx, tenantDB, db.PoolFromCtx, bus, logger, outbox.Options{})
	}

	go releaseScheduled(ctx, tenantDB, uc, logger)

	topicARN := strings.TrimSpace(os.Getenv("SES_EVENTS_TOPIC_ARN"))
	verifier := sns.NewVerifier()
	if topicARN != "" {
		if _, err := verifier.ForTopic(topicARN); err != nil {
			log.Fatalf("SES_EVENTS_TOPIC_ARN: %v", err)
		}
	}
	h := handler.NewHandler(handler.Deps{
		UC:       uc,
		TenantDB: tenantDB,
		Perms:    perms,
		SNS:      verifier,
		TopicARN: topicARN,
		Logger:   logger,
	})

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RequireGatewayToken)
	r.Use(middleware.SecureHeaders)
	r.Use(middleware.Logger(logger))
	r.Mount("/", h.Routes())

	srv := server.New(port, r, logger)
	if err := srv.Run(); err != nil {
		logger.Fatal("server error", zap.Error(err))
	}
}

// releaseScheduled encola cada minuto, empresa por empresa, los mensajes programados
// cuya hora ya paso.
func releaseScheduled(ctx context.Context, tenantDB *db.TenantDB, uc *app.UseCase, logger *zap.Logger) {
	t := time.NewTicker(releaseInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		err := tenantDB.ForEachActiveTenantConcurrent(ctx, 4, 30*time.Second, func(tctx context.Context, tenantID string) {
			id, err := uuid.Parse(tenantID)
			if err != nil {
				return
			}
			n, err := uc.ReleaseDue(tctx, id)
			if err != nil && tctx.Err() == nil {
				logger.Warn("transactional: no se liberaron los programados", zap.String("tenant_id", tenantID), zap.Error(err))
				return
			}
			if n > 0 {
				logger.Info("transactional: programados encolados", zap.String("tenant_id", tenantID), zap.Int("count", n))
			}
		})
		if err != nil && ctx.Err() == nil {
			logger.Warn("transactional: no se pudo listar las empresas", zap.Error(err))
		}
	}
}
