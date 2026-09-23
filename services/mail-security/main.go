package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
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
	"github.com/alonsosss/corforce-email/pkg/tenantcell"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/adapters/doveadm"
	handler "github.com/alonsosss/corforce-email/services/mail-security/internal/adapters/http"
	natsadapter "github.com/alonsosss/corforce-email/services/mail-security/internal/adapters/nats"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/adapters/organizationcli"
	outboxadapter "github.com/alonsosss/corforce-email/services/mail-security/internal/adapters/outbox"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/adapters/postgres"
	promadapter "github.com/alonsosss/corforce-email/services/mail-security/internal/adapters/prometheus"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/adapters/queueagent"
	redisadapter "github.com/alonsosss/corforce-email/services/mail-security/internal/adapters/redis"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/adapters/rspamd"
	smtpadapter "github.com/alonsosss/corforce-email/services/mail-security/internal/adapters/smtp"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/adapters/transactionalcli"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// Puertos: el de administracion lo enruta el gateway; los dos de los motores son los
// que la configuracion copiada de Rspamd y Postfix espera en el host mail-policy.
const (
	defaultPort       = 8042
	defaultMapsPort   = 8081
	defaultExportPort = 9081

	defaultRedisHost         = "redis"
	defaultRedisPort         = 6379
	defaultReconcileInterval = 10 * time.Minute
	defaultLogLines          = 9999
	defaultReinjectHost      = "postfix"
	defaultReinjectPort      = 590
	defaultControllerURL     = "http://rspamd:11334"
	defaultPipeMaxBodyMiB    = 50
	defaultDoveadmURL        = "https://dovecot:8443"
	defaultQueueAgentURL     = "https://postfix:8590"

	// Vigilancia de la cola de Postfix: las alertas ColaDePostfixAtascada y GestorDeColaSinRespuesta
	// (ops/observability/prometheus/rules/plataforma.yml) cuentan con una consulta por minuto como
	// mucho cada cinco: alargarla exige alargar sus umbrales.
	defaultQueuePollInterval = time.Minute
	minQueuePollInterval     = 10 * time.Second
	maxQueuePollInterval     = 5 * time.Minute

	// outboxRetention conserva lo publicado lo mismo que el stream (EnsureStream: 7 dias).
	outboxRetention = 7 * 24 * time.Hour
	streamRetry     = 5 * time.Second

	// Cada pasada de la reconciliacion reescribe desde la base todas las claves de Redis de la
	// celda y poda la cuarentena de cada empresa, con el intervalo por plazo. Es tambien lo que
	// repone mapas y claves DKIM en un redis-mail que perdio sus datos: como mucho una hora.
	minReconcileInterval = time.Minute
	maxReconcileInterval = time.Hour
	// RL_LOG se recorta a este largo en cada linea (LTRIM 0 n-1; con 0 no se recortaria). Cada
	// linea puede ocupar el cuerpo entero de /pipe_rl (256 KiB) en el Redis que Rspamd consulta en
	// cada mensaje, y el watchdog solo lee las primeras.
	maxLogLines = 10000
	// /pipe recibe de Rspamd el mensaje entero en un multipart con sus metadatos. El techo cubre el
	// mayor mensaje que acepta Postfix (message_size_limit, 100 MiB) mas 1 MiB de envoltorio y no
	// pasa de lo que Rspamd analiza (max_message): ops/scaffold/check-mail-size-limits.sh.
	maxPipeMaxBodyMiB = 101
)

// Aviso de cuarentena: cadencia del barrido, vigencia de los enlaces y cerrojo de lider
// del barrido en la base de la celda (una sola instancia avisa a la vez).
const (
	defaultQuarantineNotifyInterval       = 15 * time.Minute
	defaultQuarantineLinkTTL              = 72 * time.Hour
	quarantineNotifyLockKey         int64 = 0x716e6f7469667921 // "qnotify!"

	// Cada barrido tiene el intervalo por plazo y cada aviso, 30 s (transactionalcli): con menos
	// de un minuto no cabria ni el primero. Como mucho un dia, el menor max_age_days de una
	// empresa: con mas, la reconciliacion podria podar la cuarentena antes de avisar de ella.
	minQuarantineNotifyInterval = time.Minute
	maxQuarantineNotifyInterval = 24 * time.Hour
	// El enlace libera o descarta sin sesion: al menos una hora para leer el aviso y como mucho
	// treinta dias, lo que sirve un aviso reenviado o filtrado.
	minQuarantineLinkTTL = time.Hour
	maxQuarantineLinkTTL = 720 * time.Hour
)

// Repaso de las claves DKIM de los motores: cadencia y cerrojo de lider en la base de la celda.
const (
	defaultDKIMReconcileInterval       = 15 * time.Minute
	dkimReconcileLockKey         int64 = 0x646b696d72656321 // "dkimrec!"

	// El techo es el intervalo con que cuentan las alertas RepasoDKIMDetenido (cuatro intervalos
	// sin pasada completa) y RepasoDKIMSinOrganization, cuya ventana de 20 min debe superar un
	// intervalo: con uno mayor no avisaria nunca (ops/observability/prometheus/rules/plataforma.yml).
	// Alargarlo exige alargar a la vez sus umbrales, ventanas y esperas. El e2e repasa cada 2 s.
	minDKIMReconcileInterval = time.Second
	maxDKIMReconcileInterval = 15 * time.Minute
)

// settings es la configuracion propia del servicio, leida y validada antes de conectar a nada.
type settings struct {
	port, mapsPort, exportPort int
	redisHost                  string
	redisPort                  int
	reinjectAddr               string
	// controllerURL es el controller de Rspamd; transactionalURL, vacia, desactiva el aviso de
	// cuarentena.
	controllerURL string
	// controllerReadPassword abre la lectura del controller (estadisticas e historial) y
	// controllerLearnPassword la escritura (el aprendizaje desde la cuarentena). Vacias, cada cosa
	// queda desactivada por separado.
	controllerReadPassword   string
	controllerLearnPassword  string
	transactionalURL         string
	organizationURL          string
	internalToken            string
	perms                    *authz.Checker
	logLines                 int
	pipeMaxBodyMiB           int
	reconcileInterval        time.Duration
	queuePollInterval        time.Duration
	dkimReconcileInterval    time.Duration
	quarantineNotifyInterval time.Duration
	quarantineLinkTTL        time.Duration
}

// loadSettings falla con un valor fuera de su rango, un host o una URL mal formados y sin token
// interno fuera de desarrollo o prueba, antes de conectar a nada: el token lo presenta a
// organization y a transactional, con la misma regla que exige tenantcell.MembershipFromEnv.
func loadSettings() (settings, error) {
	var st settings
	var err error
	if st.internalToken, err = middleware.InternalGatewayToken(); err != nil {
		return st, err
	}
	if st.organizationURL, err = tenantcell.OrganizationURLFromEnv(); err != nil {
		return st, err
	}
	if st.port, err = config.EnvInt("MAIL_SECURITY_PORT", defaultPort, 1, config.MaxPort); err != nil {
		return st, err
	}
	if st.mapsPort, err = config.EnvInt("MAIL_POLICY_MAPS_PORT", defaultMapsPort, 1, config.MaxPort); err != nil {
		return st, err
	}
	if st.exportPort, err = config.EnvInt("MAIL_POLICY_EXPORT_PORT", defaultExportPort, 1, config.MaxPort); err != nil {
		return st, err
	}
	if err = distinctListenerPorts(st); err != nil {
		return st, err
	}
	if st.redisHost, err = config.EnvHost("MAIL_REDIS_HOST", defaultRedisHost); err != nil {
		return st, err
	}
	if st.redisPort, err = config.EnvInt("MAIL_REDIS_PORT", defaultRedisPort, 1, config.MaxPort); err != nil {
		return st, err
	}
	reinjectHost, err := config.EnvHost("MAIL_QUARANTINE_REINJECT_HOST", defaultReinjectHost)
	if err != nil {
		return st, err
	}
	reinjectPort, err := config.EnvInt("MAIL_QUARANTINE_REINJECT_PORT", defaultReinjectPort, 1, config.MaxPort)
	if err != nil {
		return st, err
	}
	st.reinjectAddr = net.JoinHostPort(reinjectHost, strconv.Itoa(reinjectPort))
	// El learner le pega /learnspam y manda la contrasena en su cabecera: la misma regla que una
	// URL base entre servicios, en http dentro de la red de la celda.
	if st.controllerURL, err = config.ServiceURL("RSPAMD_CONTROLLER_URL", defaultControllerURL); err != nil {
		return st, err
	}
	if st.controllerReadPassword, st.controllerLearnPassword, err = controllerPasswordsFromEnv(); err != nil {
		return st, err
	}
	if st.transactionalURL, err = config.ServiceURL("TRANSACTIONAL_URL", ""); err != nil {
		return st, err
	}
	if st.perms, err = authz.CheckerFromEnv(); err != nil {
		return st, err
	}
	if st.logLines, err = config.EnvInt("MAIL_LOG_LINES", defaultLogLines, 1, maxLogLines); err != nil {
		return st, err
	}
	if st.pipeMaxBodyMiB, err = config.EnvInt("MAIL_QUARANTINE_MAX_BODY_MB", defaultPipeMaxBodyMiB, 1, maxPipeMaxBodyMiB); err != nil {
		return st, err
	}
	if st.reconcileInterval, err = config.EnvDuration("MAIL_REDIS_RECONCILE_INTERVAL", defaultReconcileInterval, minReconcileInterval, maxReconcileInterval); err != nil {
		return st, err
	}
	if st.queuePollInterval, err = config.EnvDuration("MAIL_QUEUE_POLL_INTERVAL", defaultQueuePollInterval, minQueuePollInterval, maxQueuePollInterval); err != nil {
		return st, err
	}
	if st.dkimReconcileInterval, err = config.EnvDuration("MAIL_DKIM_RECONCILE_INTERVAL", defaultDKIMReconcileInterval, minDKIMReconcileInterval, maxDKIMReconcileInterval); err != nil {
		return st, err
	}
	if st.quarantineNotifyInterval, err = config.EnvDuration("MAIL_QUARANTINE_NOTIFY_INTERVAL", defaultQuarantineNotifyInterval, minQuarantineNotifyInterval, maxQuarantineNotifyInterval); err != nil {
		return st, err
	}
	if st.quarantineLinkTTL, err = config.EnvDuration("MAIL_QUARANTINE_LINK_TTL", defaultQuarantineLinkTTL, minQuarantineLinkTTL, maxQuarantineLinkTTL); err != nil {
		return st, err
	}
	return st, nil
}

// distinctListenerPorts: el proceso abre un listener en cada puerto. Con dos en el mismo, el segundo
// fallaria al abrirse, con los consumidores y el rele ya en marcha.
func distinctListenerPorts(st settings) error {
	listeners := []struct {
		key  string
		port int
	}{{"MAIL_SECURITY_PORT", st.port}, {"MAIL_POLICY_MAPS_PORT", st.mapsPort}, {"MAIL_POLICY_EXPORT_PORT", st.exportPort}}
	for i, a := range listeners {
		for _, b := range listeners[i+1:] {
			if a.port == b.port {
				return fmt.Errorf("%s y %s tienen el mismo puerto (%d): cada uno es un listener propio y deben ser distintos",
					a.key, b.key, a.port)
			}
		}
	}
	return nil
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// engineListener levanta un listener HTTP interno para los motores con timeouts propios:
// /pipe recibe mensajes completos y necesita mas margen que un mapa dinamico.
func engineListener(port int, h http.Handler, readTimeout time.Duration) *http.Server {
	return &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           h,
		ReadTimeout:       readTimeout,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    64 * 1024,
	}
}

// doveadmFromEnv lee el API HTTP de doveadm de la celda. Sin DOVEADM_API_KEY solo arranca con
// ENVIRONMENT=development|test, sin revocacion en Dovecot; en cualquier otro entorno no arranca:
// un buzon apagado seguiria entrando con la cache de Dovecot y sus sesiones abiertas seguirian.
func doveadmFromEnv(logger *zap.Logger) (*doveadm.Client, error) {
	// La regla de una URL base entre servicios; doveadm.New exige ademas https.
	baseURL, err := config.ServiceURL("DOVEADM_API_URL", defaultDoveadmURL)
	if err != nil {
		return nil, err
	}
	key := strings.TrimSpace(os.Getenv("DOVEADM_API_KEY"))
	if key == "" {
		if config.DeclaredDevelopmentOrTest() {
			logger.Warn("revocacion en Dovecot desactivada: falta DOVEADM_API_KEY (solo development o test)")
			return nil, nil
		}
		return nil, errors.New("DOVEADM_API_KEY es obligatoria fuera de ENVIRONMENT=development|test")
	}
	return doveadm.New(doveadm.Config{
		BaseURL:    baseURL,
		APIKey:     key,
		ServerName: envOrDefault("DOVEADM_API_TLS_SERVER_NAME", strings.TrimSpace(os.Getenv("MAIL_HOSTNAME"))),
		CAFile:     strings.TrimSpace(os.Getenv("DOVEADM_API_TLS_CA_FILE")),
	})
}

// controllerPasswordPattern: la contrasena viaja en la cabecera Password, asi que no admite nada que
// pueda romperla; es la misma regla que las otras claves compartidas con un motor.
var controllerPasswordPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{32,256}$`)

// controllerPasswordsFromEnv lee las dos contrasenas del controller de Rspamd, que genera en el motor
// deploy/mail/rspamd/controller-password.sh desde el mismo almacen. Son distintas a proposito: con una
// sola, Rspamd deja que la lectura escriba, y dar la pantalla del antispam no puede dar el aprendizaje.
func controllerPasswordsFromEnv() (read, learn string, err error) {
	read = strings.TrimSpace(os.Getenv("RSPAMD_CONTROLLER_PASSWORD"))
	learn = strings.TrimSpace(os.Getenv("RSPAMD_CONTROLLER_ENABLE_PASSWORD"))
	for name, value := range map[string]string{"RSPAMD_CONTROLLER_PASSWORD": read, "RSPAMD_CONTROLLER_ENABLE_PASSWORD": learn} {
		if value != "" && !controllerPasswordPattern.MatchString(value) {
			return "", "", fmt.Errorf("%s debe tener de 32 a 256 caracteres de [A-Za-z0-9_-]", name)
		}
	}
	if read != "" && read == learn {
		return "", "", errors.New("RSPAMD_CONTROLLER_PASSWORD y RSPAMD_CONTROLLER_ENABLE_PASSWORD no pueden ser la misma: la lectura podria escribir")
	}
	return read, learn, nil
}

// queueFromEnv lee el agente de la cola de Postfix de la celda. Sin QUEUE_AGENT_API_KEY el servicio arranca
// igual y el gestor de cola queda desactivado (sus rutas responden 503): a diferencia de la revocacion en
// Dovecot, no proteger nada depende de el, y exigirla pararia el servicio hasta que se despliegue el agente.
func queueFromEnv(logger *zap.Logger) (*queueagent.Client, error) {
	key := strings.TrimSpace(os.Getenv("QUEUE_AGENT_API_KEY"))
	if key == "" {
		logger.Info("gestor de cola de Postfix desactivado: falta QUEUE_AGENT_API_KEY")
		return nil, nil
	}
	baseURL, err := config.ServiceURL("QUEUE_AGENT_URL", defaultQueueAgentURL)
	if err != nil {
		return nil, err
	}
	return queueagent.New(queueagent.Config{
		BaseURL:    baseURL,
		APIKey:     key,
		ServerName: envOrDefault("QUEUE_AGENT_TLS_SERVER_NAME", strings.TrimSpace(os.Getenv("MAIL_HOSTNAME"))),
		CAFile:     strings.TrimSpace(os.Getenv("QUEUE_AGENT_TLS_CA_FILE")),
	})
}

// queueUseCase evita el nil tipado: un *Client nil dentro de la interfaz no seria nil y el caso de uso
// intentaria usarlo.
func queueUseCase(client *queueagent.Client, logger *zap.Logger) *app.QueueUseCase {
	if client == nil {
		return app.NewQueueUseCase(nil, logger)
	}
	return app.NewQueueUseCase(client, logger)
}

func main() {
	logger, _ := zap.NewProduction()
	defer logger.Sync()
	response.SetUnexpectedLogger(logger)

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	st, err := loadSettings()
	if err != nil {
		log.Fatalf("mail-security: %v", err)
	}
	membership, err := tenantcell.MembershipFromEnv(logger)
	if err != nil {
		log.Fatalf("celda de la instancia: %v", err)
	}
	engineSessions, err := doveadmFromEnv(logger)
	if err != nil {
		log.Fatalf("revocacion en Dovecot: %v", err)
	}
	queueAgent, err := queueFromEnv(logger)
	if err != nil {
		log.Fatalf("gestor de cola: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Servicio de CELDA: un solo pool fijo a la base de la celda. La empresa viaja en
	// el contexto (InjectFromGateway) y es lo que leen las politicas RLS.
	pool, err := db.NewCellPool(ctx, cfg.Postgres, logger)
	if err != nil {
		log.Fatalf("connect cell db: %v", err)
	}
	defer pool.Close()
	ctxPool := &db.ContextPool{}
	withPool := func(c context.Context) context.Context { return db.WithPool(c, pool.Pool) }

	// Redis de los motores (no el de la plataforma): este servicio es su unico escritor. El
	// TLS es opcional y nunca exigido: los motores lo leen en claro dentro de la red de la
	// celda (deploy/mail/README.md, Contrato Redis).
	engineRedisTLS, err := config.RedisTLSFromEnv(config.EngineRedisEnvPrefix)
	if err != nil {
		log.Fatalf("redis de los motores: %v", err)
	}
	engineTLS, err := engineRedisTLS.ClientConfig()
	if err != nil {
		log.Fatalf("redis de los motores: %v", err)
	}
	store := redisadapter.New(redisadapter.Config{
		Host:     st.redisHost,
		Port:     st.redisPort,
		Password: os.Getenv("MAIL_REDIS_PASSWORD"),
		TLS:      engineTLS,
	})
	defer store.Close()
	if err := store.Ping(ctx); err != nil {
		logger.Warn("redis de los motores no responde al arrancar; se reintentara en la reconciliacion", zap.Error(err))
	}

	// Los eventos se encolan en la outbox de la celda dentro de cada transaccion; sin NATS
	// esperan en la tabla y el rele los entrega cuando vuelva.
	publisher := outboxadapter.NewPublisher(ctxPool)
	var bus *events.Bus
	if b, err := events.NewBus(cfg.NATS.URL, logger); err != nil {
		logger.Warn("NATS no disponible: sin consumo del directorio; los eventos quedan en la outbox", zap.Error(err))
	} else {
		bus = b
		defer bus.Close()
		go runCellRelay(ctx, bus, pool, logger)
	}

	directory := postgres.NewDirectoryRepository(ctxPool)
	policyReader := postgres.NewPolicyReader(ctxPool)
	quarantineRepo := postgres.NewQuarantineRepository(ctxPool)
	redisSync := app.NewRedisSync(store, directory, policyReader, logger)

	// Claves DKIM solo de dominios activos en esta celda cuya empresa sigue existiendo.
	metrics := promadapter.New()
	dkimUC := app.NewDKIMUseCase(app.DKIMDeps{
		Directory: directory, Lock: postgres.NewDKIMLock(ctxPool), Sync: redisSync, Logger: logger,
		Tenants: organizationcli.New(tenantcell.NewResolver(st.organizationURL, st.internalToken, logger)),
		Metrics: metrics,
	})

	// Enlaces sin sesion del aviso de cuarentena: firmados con MAIL_LINK_SIGNING_KEY,
	// colgados de PUBLIC_BASE_URL y atados a la celda CELL_CODE, que va en su ruta para que el
	// gateway los enrute. Sin ellos el servicio arranca, pero no avisa y todo enlace es
	// invalido.
	noticeRepo := postgres.NewQuarantineNoticeRepository(ctxPool)
	quarantineLinks, linksErr := domain.NewQuarantineLinkSigner(os.Getenv("MAIL_LINK_SIGNING_KEY"), os.Getenv("PUBLIC_BASE_URL"),
		strings.TrimSpace(os.Getenv("CELL_CODE")), st.quarantineLinkTTL)
	if linksErr != nil {
		logger.Error("aviso de cuarentena y sus enlaces desactivados", zap.Error(linksErr))
	}

	policyUC := app.NewPolicyUseCase(app.PolicyDeps{
		Tx: ctxPool, Repo: postgres.NewPolicyRepository(ctxPool), Directory: directory, Sync: redisSync, Logger: logger,
	})
	// Controller de Rspamd, con una contrasena por permiso (docs/adr/0009): la de escritura entrena desde la
	// cuarentena y la de lectura sirve al superadmin sus contadores e historial. Sin la suya, cada una
	// responde 503 NOT_CONFIGURED por separado.
	learner := rspamd.New(st.controllerURL, st.controllerLearnPassword)
	antispamReader := rspamd.New(st.controllerURL, st.controllerReadPassword)
	logger.Info("controller de Rspamd",
		zap.Bool("lectura", st.controllerReadPassword != ""), zap.Bool("aprendizaje", st.controllerLearnPassword != ""))
	quarantineUC := app.NewQuarantineUseCase(app.QuarantineDeps{
		Tx:         ctxPool,
		Repo:       quarantineRepo,
		Reinjector: smtpadapter.New(st.reinjectAddr, envOrDefault("MAIL_HOSTNAME", "mail-security")),
		Learner:    learner,
		Events:     publisher,
		Notices:    noticeRepo,
		Links:      quarantineLinks,
		Metrics:    metrics,
		Logger:     logger,
	})
	engineUC := app.NewEngineUseCase(app.EngineDeps{
		Tx: ctxPool, Documents: postgres.NewDocumentRepository(ctxPool),
		Directory: directory, Policy: policyReader, Quarantine: quarantineRepo, Sync: redisSync,
		Store: store, Events: publisher, Metrics: metrics, Logger: logger, LogLines: int64(st.logLines),
	})
	firewallUC := app.NewFirewallUseCase(app.FirewallDeps{
		Tx: ctxPool, Repo: postgres.NewFirewallRepository(ctxPool), Policy: policyReader, Sync: redisSync, Store: store, Logger: logger,
	})

	// Reconciliacion de Redis al arrancar y periodica, y consumo de eventos del
	// directorio. Ambos corren fuera de cualquier peticion: el pool va en el contexto.
	go app.NewReconciler(redisSync, policyReader, quarantineRepo, st.reconcileInterval, logger).Run(withPool(ctx))
	go natsadapter.NewDirectoryConsumer(bus, redisSync, dkimUC, policyReader, withPool, logger).Run(ctx)
	// Revocacion en Dovecot: con cada evento de buzon vacia su cache de autenticacion y, si ya no
	// puede entrar o cambio su credencial, cierra sus sesiones (deploy/mail/README.md).
	if engineSessions != nil {
		revoker := app.NewSessionRevoker(app.SessionRevokerDeps{Directory: directory, Engine: engineSessions, Metrics: metrics, Logger: logger})
		go natsadapter.NewSessionConsumer(bus, revoker, withPool, logger).Run(ctx)
	}
	// Vigilancia de la cola de Postfix, solo con el agente configurado.
	if queueAgent != nil {
		go app.NewQueueMonitor(queueAgent, metrics, st.queuePollInterval, logger).Run(ctx)
	}
	go dkimUC.RunReconciler(withPool(ctx), st.dkimReconcileInterval,
		func(c context.Context) (func(), bool) { return db.TryLeaderLock(c, pool.Pool, dkimReconcileLockKey) })

	// Aviso de cuarentena por transactional (POST /internal/transactional/messages con
	// purpose=quarantine_notice), con el cerrojo de lider de la celda.
	switch {
	case linksErr != nil:
		// Ya registrado al construir el firmante: sin enlaces no se avisa.
	case st.transactionalURL == "" || st.internalToken == "":
		logger.Error("aviso de cuarentena desactivado: faltan TRANSACTIONAL_URL o INTERNAL_GATEWAY_TOKEN")
	default:
		notifier := app.NewQuarantineNotifier(app.NotifierDeps{
			Tx: ctxPool, Policy: policyReader, Notices: noticeRepo, Directory: directory,
			Sender: transactionalcli.New(st.transactionalURL, st.internalToken), Links: quarantineLinks,
			Interval: st.quarantineNotifyInterval, Logger: logger,
		})
		go notifier.Run(withPool(ctx), func(c context.Context) (func(), bool) {
			return db.TryLeaderLock(c, pool.Pool, quarantineNotifyLockKey)
		})
	}

	// Superficie A: API de administracion tras el gateway (y rutas internas con token). El
	// cortafuegos es de plataforma: el superadmin lo opera en cualquier celda con celda destino.
	routes := handler.NewHandler(policyUC, quarantineUC, firewallUC, dkimUC, st.perms).
		WithQueue(queueUseCase(queueAgent, logger)).
		WithAntispam(app.NewAntispamUseCase(antispamReader, logger)).
		Routes()
	if err := membership.AcceptOperators(routes, handler.PlatformRoutes()); err != nil {
		log.Fatalf("celda de la instancia: %v", err)
	}
	r := apiRouter(pool.Pool, membership, routes, logger)

	// Superficie B: listeners de los motores, sin gateway ni JWT, acotados por IP. No llevan
	// empresa (Postfix, Dovecot y Rspamd no saben de ellas): no pasan por Membership.
	allowedCIDRs := os.Getenv("MAIL_ENGINE_ALLOWED_CIDRS")
	pipeMaxBody := int64(st.pipeMaxBodyMiB) * 1024 * 1024
	maps := engineListener(st.mapsPort,
		db.StaticPoolMiddleware(pool.Pool)(handler.NewEngineHandler(engineUC, logger).Routes(allowedCIDRs)), 15*time.Second)
	export := engineListener(st.exportPort,
		db.StaticPoolMiddleware(pool.Pool)(handler.NewExporterHandler(engineUC, pipeMaxBody, logger).Routes(allowedCIDRs)), 120*time.Second)
	for _, srv := range []*http.Server{maps, export} {
		go func(s *http.Server) {
			logger.Info("engine listener starting", zap.String("addr", s.Addr))
			if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Fatal("engine listener failed", zap.String("addr", s.Addr), zap.Error(err))
			}
		}(srv)
	}

	srv := server.New(st.port, r, logger)
	runErr := srv.Run()

	// El servidor principal ya atendio la senal de parada: se apagan los listeners de
	// los motores con el mismo margen y se detienen las tareas de fondo.
	cancel()
	shutdownCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	for _, s := range []*http.Server{maps, export} {
		if err := s.Shutdown(shutdownCtx); err != nil {
			logger.Warn("engine listener shutdown", zap.String("addr", s.Addr), zap.Error(err))
		}
	}
	if runErr != nil {
		logger.Fatal("server error", zap.Error(runErr))
	}
}

// apiRouter monta la superficie A: el API de administracion que llega por el gateway, los
// enlaces publicos de cuarentena y las rutas internas de domain-service, todo tras el token
// interno. Una peticion por una empresa que no es de esta celda se rechaza antes de llegar a
// ninguna ruta (tenantcell.Membership).
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
// y consume los reintentos de la fila. Comparte cerrojo con el rele de mail-directory, de
// modo que en toda la celda solo vacia una instancia a la vez.
func runCellRelay(ctx context.Context, bus *events.Bus, pool *db.Pool, logger *zap.Logger) {
	for {
		err := bus.EnsureStream(domain.StreamName, []string{domain.SubjectQuarantineStored, domain.SubjectQuarantineReleased})
		if err == nil {
			break
		}
		logger.Warn("stream MAIL_SECURITY no asegurado; se reintenta", zap.Error(err))
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
