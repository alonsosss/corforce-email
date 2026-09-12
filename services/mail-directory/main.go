package main

import (
	"context"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/config"
	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/server"
	handler "github.com/alonsosss/corforce-email/services/mail-directory/internal/adapters/http"
	natsadapter "github.com/alonsosss/corforce-email/services/mail-directory/internal/adapters/nats"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/adapters/postgres"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/adapters/secrets"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/app"
	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

const defaultPort = 8040

// mail-directory es un servicio de CELDA: una sola base (la del directorio de correo que
// leen Postfix y Dovecot) compartida por todas las empresas de la celda. No hay pool por
// empresa; el aislamiento lo dan tenant_id en cada consulta y RLS bajo el rol mail_app.
func main() {
	logger, _ := zap.NewProduction()
	defer logger.Sync()
	response.SetUnexpectedLogger(logger)

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	dsn, err := cfg.Postgres.CellDSN()
	if err != nil {
		log.Fatalf("cell dsn: %v", err)
	}

	ctx := context.Background()
	pool, err := db.NewNamedPool(ctx, dsn, "cell", logger)
	if err != nil {
		log.Fatalf("connect cell db: %v", err)
	}
	defer pool.Close()
	ctxPool := &db.ContextPool{}

	// Sin NATS el directorio sigue operando; los consumidores (mail-security) se quedan
	// sin avisos hasta que vuelva, que es preferible a no poder administrar buzones.
	publisher := natsadapter.NewPublisher(nil)
	if bus, err := events.NewBus(cfg.NATS.URL, logger); err != nil {
		logger.Warn("NATS no disponible: los eventos del directorio no se publicaran", zap.Error(err))
	} else {
		defer bus.Close()
		if err := bus.EnsureStream(natsadapter.StreamName, []string{natsadapter.StreamSubjects}); err != nil {
			logger.Warn("ensure stream MAIL_DIRECTORY", zap.Error(err))
		}
		publisher = natsadapter.NewPublisher(bus)
	}

	uc := app.New(app.Deps{
		Tx:           postgres.NewTransactor(ctxPool),
		Domains:      postgres.NewDomainRepo(ctxPool),
		AliasDomains: postgres.NewAliasDomainRepo(ctxPool),
		Mailboxes:    postgres.NewMailboxRepo(ctxPool),
		AppPasswords: postgres.NewAppPasswordRepo(ctxPool),
		Sieve:        postgres.NewSieveRepo(ctxPool),
		Aliases:      postgres.NewAliasRepo(ctxPool),
		SpamAliases:  postgres.NewSpamAliasRepo(ctxPool),
		SenderACL:    postgres.NewSenderACLRepo(ctxPool),
		Relayhosts:   postgres.NewRelayhostRepo(ctxPool),
		Transports:   postgres.NewTransportRepo(ctxPool),
		TLSPolicies:  postgres.NewTLSPolicyRepo(ctxPool),
		RecipientMap: postgres.NewRecipientMapRepo(ctxPool),
		BCCMaps:      postgres.NewBCCMapRepo(ctxPool),
		Secrets:      secrets.New(),
		Events:       publisher,
		Logger:       logger,
	})

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RequireGatewayToken)
	r.Use(middleware.InjectFromGateway)
	r.Use(db.StaticPoolMiddleware(pool.Pool))
	r.Use(middleware.SecureHeaders)
	r.Use(middleware.Logger(logger))
	r.Use(middleware.NewRateLimiter(120, time.Minute).Limit)
	r.Mount("/", handler.NewHandler(uc, authz.NewCheckerFromEnv()).Routes())

	port := defaultPort
	if p := os.Getenv("MAIL_DIRECTORY_PORT"); p != "" {
		if v, err := strconv.Atoi(p); err == nil {
			port = v
		}
	}

	srv := server.New(port, r, logger)
	if err := srv.Run(); err != nil {
		logger.Fatal("server error", zap.Error(err))
	}
}
