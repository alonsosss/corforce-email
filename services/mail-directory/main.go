package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
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
	"github.com/alonsosss/corforce-email/pkg/tenantcell"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/adapters/billingcli"
	dnsadapter "github.com/alonsosss/corforce-email/services/mail-directory/internal/adapters/dns"
	handler "github.com/alonsosss/corforce-email/services/mail-directory/internal/adapters/http"
	outboxadapter "github.com/alonsosss/corforce-email/services/mail-directory/internal/adapters/outbox"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/adapters/postgres"
	prometheusadapter "github.com/alonsosss/corforce-email/services/mail-directory/internal/adapters/prometheus"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/adapters/secrets"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

const (
	defaultPort = 8040
	// outboxRetention conserva lo publicado lo mismo que el stream (EnsureStream: 7 dias),
	// para poder reconstruir una entrega perdida mientras JetStream aun la recuerda.
	outboxRetention = 7 * 24 * time.Hour
	streamRetry     = 5 * time.Second
	// Con llaves retiradas en MAIL_ENCRYPTION_KEYS_OLD, el secreto de la verificacion en dos pasos
	// se re-cifra bajo la activa al arrancar y despues cada hora, en lotes.
	mfaRotationEvery = time.Hour
	mfaRotationBatch = 200
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
	port, err := config.EnvInt("MAIL_DIRECTORY_PORT", defaultPort, 1, config.MaxPort)
	if err != nil {
		log.Fatal(err)
	}
	perms, err := authz.CheckerFromEnv()
	if err != nil {
		log.Fatal(err)
	}
	membership, err := tenantcell.MembershipFromEnv(logger)
	if err != nil {
		log.Fatalf("celda de la instancia: %v", err)
	}
	platformMX, err := domain.NormalizeDomain(os.Getenv("MAIL_MX_HOSTNAME"))
	if err != nil {
		log.Fatalf("MAIL_MX_HOSTNAME %q: %v", os.Getenv("MAIL_MX_HOSTNAME"), err)
	}
	davServerURL, err := davServerURLFromEnv()
	if err != nil {
		log.Fatal(err)
	}
	// Retencion de una direccion recien borrada: mayor que la gracia mas la cadencia del barrido de
	// maildir de Dovecot (deploy/mail/README.md, "Maildir de un buzon borrado"); 0 la desactiva.
	recreateHold, err := config.EnvDuration("MAIL_DIRECTORY_MAILBOX_RECREATE_HOLD", 15*time.Minute, 0, 24*time.Hour)
	if err != nil {
		log.Fatal(err)
	}
	// Cifra el secreto de la verificacion en dos pasos de cada buzon. Obligatoria, como en
	// domain-service: sin ella nadie podria activar ni superar el segundo paso.
	keyRing, err := crypto.LoadKeyRing("MAIL_ENCRYPTION_KEY", "MAIL_ENCRYPTION_KEYS_OLD")
	if err != nil {
		log.Fatalf("mail-directory: cifrado del secreto de la verificación en dos pasos: %v", err)
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

	// Limites del plan de cada empresa (billing). Opcional a proposito: sin BILLING_URL el
	// directorio aplica solo los limites del dominio, como antes de que hubiera planes, y lo
	// avisa al arrancar en vez de negarse a servir.
	planLimits := billingcli.New(os.Getenv("BILLING_URL"), os.Getenv("INTERNAL_GATEWAY_TOKEN"))
	metrics := prometheusadapter.New()
	metrics.PlanLimitsConfigured(planLimits.Configured())
	if !planLimits.Configured() {
		logger.Warn("mail-directory: sin BILLING_URL no se aplican los limites del plan a los buzones ni al espacio")
	}

	uc := app.New(app.Deps{
		Tx:                  postgres.NewTransactor(ctxPool),
		Domains:             postgres.NewDomainRepo(ctxPool),
		AliasDomains:        postgres.NewAliasDomainRepo(ctxPool),
		Mailboxes:           postgres.NewMailboxRepo(ctxPool),
		AppPasswords:        postgres.NewAppPasswordRepo(ctxPool),
		Sieve:               postgres.NewSieveRepo(ctxPool),
		Vacation:            postgres.NewVacationRepo(ctxPool),
		Signatures:          postgres.NewSignatureRepo(ctxPool),
		Assistant:           postgres.NewAssistantSettingsRepo(ctxPool),
		MFA:                 postgres.NewMFARepo(ctxPool),
		Sealer:              secrets.NewKeyRingSealer(keyRing),
		TOTP:                secrets.TOTP{},
		Policies:            postgres.NewMailPolicyRepo(ctxPool),
		Filters:             postgres.NewFilterRepo(ctxPool),
		Scheduled:           postgres.NewScheduledSendRepo(ctxPool),
		Reminders:           postgres.NewReminderRepo(ctxPool),
		QuickReplies:        postgres.NewQuickReplyRepo(ctxPool),
		Locator:             postgres.NewMailboxLocator(ctxPool),
		Aliases:             postgres.NewAliasRepo(ctxPool),
		SpamAliases:         postgres.NewSpamAliasRepo(ctxPool),
		SenderACL:           postgres.NewSenderACLRepo(ctxPool),
		Relayhosts:          postgres.NewRelayhostRepo(ctxPool),
		Transports:          postgres.NewTransportRepo(ctxPool),
		TLSPolicies:         postgres.NewTLSPolicyRepo(ctxPool),
		RecipientMap:        postgres.NewRecipientMapRepo(ctxPool),
		BCCMaps:             postgres.NewBCCMapRepo(ctxPool),
		Senders:             postgres.NewSenderIdentityRepo(ctxPool),
		Retirements:         postgres.NewRetirementRepo(ctxPool),
		MTASTS:              postgres.NewMTASTSRepo(ctxPool),
		MTASTSPublic:        postgres.NewMTASTSPublicReader(ctxPool),
		MX:                  dnsadapter.New(strings.TrimSpace(os.Getenv("MAIL_DNS_RESOLVER"))),
		PlatformMX:          platformMX,
		DAVServerURL:        davServerURL,
		MailboxRecreateHold: recreateHold,
		Secrets:             secrets.New(),
		Events:              outboxadapter.NewPublisher(ctxPool),
		Plan:                planLimits,
		Metrics:             metrics,
		Logger:              logger,
	})

	if keyRing.HasOldKeys() {
		go runMFASecretRotation(ctx, keyRing, postgres.NewMFASecretStore(pool.Pool), metrics, logger)
	}

	r := apiRouter(pool.Pool, membership, handler.NewHandler(uc, perms).Routes(), logger)

	srv := server.New(port, r, logger)
	runErr := srv.Run()
	cancel()
	if runErr != nil {
		logger.Fatal("server error", zap.Error(runErr))
	}
}

// davServerURLFromEnv lee MAIL_DAV_PUBLIC_URL, la URL con la que un cliente CardDAV llega a mail-dav por el
// gateway. Es opcional: vacia, el directorio no ofrece datos de conexion. Con valor exige https (http solo
// en desarrollo y prueba), sin credenciales, consulta ni fragmento, y la devuelve con barra final.
func davServerURLFromEnv() (string, error) {
	raw := strings.TrimSpace(os.Getenv("MAIL_DAV_PUBLIC_URL"))
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return "", fmt.Errorf("MAIL_DAV_PUBLIC_URL %q no es una URL https sin credenciales, consulta ni fragmento", raw)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && config.DeclaredDevelopmentOrTest()) {
		return "", fmt.Errorf("MAIL_DAV_PUBLIC_URL %q debe ser https (http solo con ENVIRONMENT development o test)", raw)
	}
	if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	return u.String(), nil
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

// runMFASecretRotation re-cifra bajo la llave activa los secretos de la verificacion en dos pasos
// que solo abre una llave retirada, al arrancar y despues cada mfaRotationEvery. Nunca registra un
// secreto: solo cuentas. Mientras diga pendientes > 0 no se puede retirar la llave vieja
// (docs/Operacion_Despliegue.md, seccion 2, rotacion de MAIL_ENCRYPTION_KEY).
func runMFASecretRotation(ctx context.Context, keyRing *crypto.KeyRing, store *postgres.MFASecretStore,
	metrics *prometheusadapter.Metrics, logger *zap.Logger) {
	t := time.NewTicker(mfaRotationEvery)
	defer t.Stop()
	for {
		rep, err := crypto.RotateStore(ctx, keyRing, store, uuid.Nil, mfaRotationBatch, domain.MFASecretAAD)
		metrics.MFASecretsReencrypted(rep.Rotated)
		switch {
		case err != nil && ctx.Err() == nil:
			logger.Warn("rotacion de MAIL_ENCRYPTION_KEY en mail.mailbox_mfa interrumpida; se reintenta en la proxima pasada",
				zap.Int("recifrados", rep.Rotated), zap.Error(err))
		case err == nil:
			metrics.MFASecretsPending(rep.Pending)
			logger.Info(fmt.Sprintf("rotacion de MAIL_ENCRYPTION_KEY en mail.mailbox_mfa: %d re-cifrados, %d pendientes", rep.Rotated, rep.Pending),
				zap.Int("revisados", rep.Examined), zap.Int("recifrados", rep.Rotated),
				zap.Int("cambiados_entre_tanto", rep.Changed), zap.Int("pendientes", rep.Pending))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
