package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/config"
	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/outbox"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/server"
	"github.com/alonsosss/corforce-email/pkg/tenantcell"
	handler "github.com/alonsosss/corforce-email/services/mail-directory/internal/adapters/http"
	outboxadapter "github.com/alonsosss/corforce-email/services/mail-directory/internal/adapters/outbox"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/adapters/postgres"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/adapters/secrets"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/app"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

const (
	defaultPort = 8040
	// outboxRetention conserva lo publicado lo mismo que el stream (EnsureStream: 7 dias),
	// para poder reconstruir una entrega perdida mientras JetStream aun la recuerda.
	outboxRetention = 7 * 24 * time.Hour
	streamRetry     = 5 * time.Second
)

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
	membership, err := tenantcell.MembershipFromEnv(logger)
	if err != nil {
		log.Fatalf("celda de la instancia: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool, err := db.NewCellPool(ctx, cfg.Postgres, logger)
	if err != nil {
		log.Fatalf("connect cell db: %v", err)
	}
	defer pool.Close()
	ctxPool := &db.ContextPool{}

	// Los eventos se encolan en la outbox de la celda dentro de cada transaccion; sin NATS
	// el directorio sigue operando y los eventos esperan en la tabla a que vuelva.
	if bus, err := events.NewBus(cfg.NATS.URL, logger); err != nil {
		logger.Warn("NATS no disponible: los eventos del directorio quedan en la outbox", zap.Error(err))
	} else {
		defer bus.Close()
		go runCellRelay(ctx, bus, pool, logger)
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
		Senders:      postgres.NewSenderIdentityRepo(ctxPool),
		Retirements:  postgres.NewRetirementRepo(ctxPool),
		Secrets:      secrets.New(),
		Events:       outboxadapter.NewPublisher(ctxPool),
		Logger:       logger,
	})

	r := apiRouter(pool.Pool, membership, handler.NewHandler(uc, authz.NewCheckerFromEnv()).Routes(), logger)

	port := defaultPort
	if p := os.Getenv("MAIL_DIRECTORY_PORT"); p != "" {
		if v, err := strconv.Atoi(p); err == nil {
			port = v
		}
	}

	srv := server.New(port, r, logger)
	runErr := srv.Run()
	cancel()
	if runErr != nil {
		logger.Fatal("server error", zap.Error(runErr))
	}
}

// apiRouter monta todo lo que el servicio sirve tras el token interno: el API con sesion que
// llega por el gateway y las rutas servicio a servicio. Una peticion por una empresa que no es
// de esta celda se rechaza antes de llegar a ninguna ruta (tenantcell.Membership).
func apiRouter(pool *pgxpool.Pool, membership *tenantcell.Membership, routes http.Handler, logger *zap.Logger) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RequireGatewayToken)
	r.Use(middleware.InjectFromGateway)
	r.Use(db.StaticPoolMiddleware(pool))
	r.Use(middleware.SecureHeaders)
	r.Use(middleware.Logger(logger))
	r.Use(middleware.NewRateLimiter(120, time.Minute).Limit)
	r.Use(membership.Require)
	r.Mount("/", routes)
	return r
}

// runCellRelay vacia la outbox de la celda hacia JetStream. Antes asegura el stream de sus
// subjects, reintentando mientras NATS no responda: publicar en un subject sin stream falla
// y consume los reintentos de la fila. Comparte cerrojo con el rele de mail-security, de
// modo que en toda la celda solo vacia una instancia a la vez.
func runCellRelay(ctx context.Context, bus *events.Bus, pool *db.Pool, logger *zap.Logger) {
	for {
		err := bus.EnsureStream(outboxadapter.StreamName, []string{outboxadapter.StreamSubjects})
		if err == nil {
			break
		}
		logger.Warn("stream MAIL_DIRECTORY no asegurado; se reintenta", zap.Error(err))
		select {
		case <-ctx.Done():
			return
		case <-time.After(streamRetry):
		}
	}
	relay := outbox.NewRelay(pool.Pool, bus, logger, outbox.Options{Retention: outboxRetention})
	relay.RunExclusive(ctx, func(c context.Context) (func(), bool) {
		return db.TryLeaderLock(c, pool.Pool, outbox.CellRelayLockKey)
	})
}
