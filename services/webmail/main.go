// webmail es el cliente web del correo corporativo de la celda: lee y organiza el buzon
// por IMAP contra Dovecot y envia por el submission de Postfix, con sesion propia en
// Redis. Autentica contra el BUZON (mail-auth, service "webmail"), no contra identity.
//
// Sin tablas propias: la sesion vive en Redis y el correo en Dovecot. Se expone por el
// gateway como prefijo autenticado por el servicio (routes.json, self_authenticated).
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	// La base de zonas va en el binario (imagen scratch): los correos de invitacion escriben la hora en la zona
	// del evento.
	_ "time/tzdata"

	"github.com/alonsosss/corforce-email/pkg/config"
	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/server"
	"github.com/alonsosss/corforce-email/services/webmail/internal/adapters/clamav"
	"github.com/alonsosss/corforce-email/services/webmail/internal/adapters/htmlsafe"
	handler "github.com/alonsosss/corforce-email/services/webmail/internal/adapters/http"
	imapadapter "github.com/alonsosss/corforce-email/services/webmail/internal/adapters/imap"
	"github.com/alonsosss/corforce-email/services/webmail/internal/adapters/mailauth"
	"github.com/alonsosss/corforce-email/services/webmail/internal/adapters/maildavcli"
	"github.com/alonsosss/corforce-email/services/webmail/internal/adapters/maildirectorycli"
	"github.com/alonsosss/corforce-email/services/webmail/internal/adapters/mailfilescli"
	natsadapter "github.com/alonsosss/corforce-email/services/webmail/internal/adapters/nats"
	promadapter "github.com/alonsosss/corforce-email/services/webmail/internal/adapters/prometheus"
	redisadapter "github.com/alonsosss/corforce-email/services/webmail/internal/adapters/redis"
	"github.com/alonsosss/corforce-email/services/webmail/internal/adapters/rfc5322"
	smtpadapter "github.com/alonsosss/corforce-email/services/webmail/internal/adapters/smtp"
	"github.com/alonsosss/corforce-email/services/webmail/internal/adapters/unsubscribe"
	"github.com/alonsosss/corforce-email/services/webmail/internal/app"
	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"github.com/alonsosss/corforce-email/services/webmail/internal/ports"
	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

const (
	defaultPort          = 8044
	defaultSessionIdle   = 30 * time.Minute
	defaultSessionMax    = 12 * time.Hour
	defaultMaxRecipients = 100

	// Avisos en tiempo real: cada buzon vigilado mantiene una sesion IMAP en IDLE (un proceso imap de
	// Dovecot, del orden de 5 a 10 MB) compartida por todas sus pestanas. El tope de buzones acota esa
	// memoria en la celda; 0 desactiva los avisos y la interfaz refresca por sondeo.
	defaultEventsMaxPerMailbox = 5
	maxEventsMaxPerMailbox     = 20
	defaultEventsMaxMailboxes  = 100
	maxEventsMaxMailboxes      = 5000
	defaultMaxMessageBytes     = 25 << 20
	defaultMaxBodyPartBytes    = 2 << 20
	defaultMaxAttachmentBytes  = 50 << 20

	// Envio programado: cada cuanto el trabajador reclama filas vencidas y cuantas a la vez, y lo
	// mas lejos que se puede programar. El intervalo es el retraso maximo con el que sale un envio
	// respecto de su hora. El lote se envia en paralelo y cada envio en curso puede ocupar en memoria
	// unas cuatro veces WEBMAIL_MAX_MESSAGE_BYTES (mensaje leido, copia y version sin Bcc): el tope
	// del lote acota esa memoria dentro del limite del contenedor.
	defaultScheduledPollInterval = 15 * time.Second
	minScheduledPollInterval     = time.Second
	maxScheduledPollInterval     = 5 * time.Minute
	defaultScheduledBatch        = 4
	maxScheduledBatch            = 20
	// Envios y borradores interactivos que se componen a la vez; el resto recibe 503 con
	// Retry-After. Se suma a la memoria del lote programado, asi que ambos se dimensionan juntos.
	defaultComposeConcurrency = 2
	maxComposeConcurrency     = 32
	defaultScheduledMaxDays   = 365
	// maxScheduledMaxDays queda por debajo de lo que admite mail-directory (366 dias): una hora
	// que el webmail acepta nunca la rechaza el directorio.
	maxScheduledMaxDays = 365

	// Recordatorios (posponer y seguimiento): como el envio programado, el intervalo es el retraso
	// maximo con el que vuelve un pospuesto o se avisa de un seguimiento. Cada recordatorio del lote
	// ocupa una conexion IMAP; el de seguimiento lee en memoria como mucho un mensaje enviado.
	defaultRemindersPollInterval = 30 * time.Second
	minRemindersPollInterval     = time.Second
	maxRemindersPollInterval     = 5 * time.Minute
	defaultRemindersBatch        = 4
	maxRemindersBatch            = 20
	defaultRemindersMaxDays      = 365
	// maxRemindersMaxDays queda por debajo de lo que admite mail-directory (366 dias).
	maxRemindersMaxDays = 365

	// Importacion de la libreta personal: el fichero vCard se lee entero en memoria.
	defaultMaxImportBytes = 5 << 20
	maxMaxImportBytes     = 50 << 20

	// Topes de los motores de la celda, por donde entra y sale todo mensaje: el
	// message_size_limit de deploy/mail/postfix/conf/main.cf.base (ningun mensaje, ni por tanto
	// ninguna de sus partes, lo supera) y el smtpd_recipient_limit de Postfix, que main.cf
	// deja en su valor por defecto. message_size_limit es ademas lo que clamd analiza entero
	// (StreamMaxLength y MaxFileSize de deploy/mail/clamav/clamd.conf, 101 MiB): ningun tope
	// configurable puede pasar de ahi o un adjunto se quedaria sin veredicto.
	// ops/scaffold/check-mail-size-limits.sh compara esta constante con los dos ficheros.
	postfixMessageSizeLimit = 100 << 20
	postfixRecipientLimit   = 1000
	maxSessionIdle          = 24 * time.Hour
	maxSessionMax           = 30 * 24 * time.Hour

	// minMasterPasswordLen coincide con lo que exige el entrypoint de Dovecot: la
	// credencial maestra abre cualquier buzon de la celda.
	minMasterPasswordLen = 32

	operationTimeout   = 60 * time.Second
	transferTimeout    = 5 * time.Minute
	mailAuthTimeout    = 15 * time.Second
	clamdTimeout       = 60 * time.Second
	rateLimitPerMinute = 600

	// unsubscribeTimeout acota de principio a fin la baja en un clic contra el servidor del boletin.
	unsubscribeTimeout = 10 * time.Second
)

// largeFileCeiling es lo que se deja pasar hacia mail-files en una subida de fichero grande: el mismo
// techo que clamd analiza entero, que mail-files aplica tambien al suyo (MAIL_FILES_MAX_FILE_BYTES).
const largeFileCeiling = postfixMessageSizeLimit

type settings struct {
	port               int
	cellCode           string
	mailAuthURL        string
	imapAddr           string
	imapTLS            string
	smtpAddr           string
	smtpTLS            string
	tlsServerName      string
	tlsCAFile          string
	tlsInsecure        bool
	masterUser         string
	masterPassword     string
	sessions           domain.SessionPolicy
	limits             domain.Limits
	maxBodyPartBytes   int64
	maxAttachmentBytes int64
	clamdAddr          string
	cookieSecure       bool
	origins            []string
	heloName           string
	mailDirectoryURL   string
	mailDavURL         string
	mailFilesURL       string
	internalToken      string
	scheduledPoll      time.Duration
	scheduledBatch     int
	composeConcurrency int
	scheduledMaxDays   int
	remindersPoll      time.Duration
	remindersBatch     int
	remindersMaxDays   int
	maxImportBytes     int64
	eventsPerMailbox   int
	eventsMailboxes    int
}

func main() {
	logger, _ := zap.NewProduction()
	response.SetUnexpectedLogger(logger)
	defer logger.Sync()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	st, err := loadSettings()
	if err != nil {
		log.Fatalf("webmail: %v", err)
	}
	assistantCfg, err := loadAssistantSettings()
	if err != nil {
		log.Fatalf("webmail: %v", err)
	}
	if st.tlsInsecure {
		logger.Warn("webmail: WEBMAIL_TLS_INSECURE_SKIP_VERIFY=true, no se verifican los certificados de Dovecot, Postfix ni mail-auth (solo desarrollo)")
	}
	if st.imapTLS == imapadapter.TLSNone {
		logger.Warn("webmail: IMAP sin TLS (solo desarrollo)")
	}

	engineTLS, err := tlsConfig(st.tlsServerName, st.tlsCAFile, st.tlsInsecure)
	if err != nil {
		log.Fatalf("webmail: TLS de los motores: %v", err)
	}
	authURL, err := url.Parse(st.mailAuthURL)
	if err != nil {
		log.Fatalf("webmail: MAIL_AUTH_URL: %v", err)
	}
	authTLS, err := tlsConfig(authURL.Hostname(), st.tlsCAFile, st.tlsInsecure)
	if err != nil {
		log.Fatalf("webmail: TLS de mail-auth: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	redisTLS, err := cfg.Redis.TLSConfig()
	if err != nil {
		log.Fatalf("webmail: redis: %v", err)
	}
	rdb := goredis.NewClient(&goredis.Options{Addr: cfg.Redis.Addr(), Password: cfg.Redis.Password, TLSConfig: redisTLS})
	defer rdb.Close()
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("webmail: Redis no disponible (las sesiones viven ahi): %v", err)
	}

	authClient, err := mailauth.New(st.mailAuthURL, authTLS, mailAuthTimeout)
	if err != nil {
		log.Fatalf("webmail: %v", err)
	}
	store, err := imapadapter.NewStore(imapadapter.Config{
		Addr: st.imapAddr, TLSMode: st.imapTLS, TLSConfig: engineTLS,
		MasterUser: st.masterUser, MasterPassword: st.masterPassword,
	}, logger)
	if err != nil {
		log.Fatalf("webmail: %v", err)
	}
	sender, err := smtpadapter.NewSender(smtpadapter.Config{
		Addr: st.smtpAddr, TLSMode: st.smtpTLS, TLSConfig: engineTLS,
		MasterUser: st.masterUser, MasterPassword: st.masterPassword,
		HeloName: st.heloName, Timeout: transferTimeout,
	}, logger)
	if err != nil {
		log.Fatalf("webmail: %v", err)
	}
	directory, err := maildirectorycli.New(st.mailDirectoryURL, st.internalToken)
	if err != nil {
		log.Fatalf("webmail: %v", err)
	}
	dav, err := maildavcli.New(st.mailDavURL, st.internalToken, transferTimeout)
	if err != nil {
		log.Fatalf("webmail: %v", err)
	}
	var largeFiles ports.LargeFiles
	if st.mailFilesURL != "" {
		if largeFiles, err = mailfilescli.New(st.mailFilesURL, st.internalToken, transferTimeout); err != nil {
			log.Fatalf("webmail: %v", err)
		}
	} else {
		logger.Warn("webmail: MAIL_FILES_URL sin definir; el envio de ficheros grandes por enlace queda desactivado")
	}
	var scanner ports.VirusScanner
	if st.clamdAddr != "" {
		scanner = clamav.New(st.clamdAddr, clamdTimeout)
	} else {
		logger.Warn("webmail: WEBMAIL_ALLOW_UNSCANNED_ATTACHMENTS=true, los adjuntos NO se analizan con ClamAV (solo desarrollo)")
	}

	var watcher ports.MailboxWatcher
	if st.eventsMailboxes > 0 {
		watcher = imapadapter.NewWatcher(store, imapadapter.WatchConfig{MaxPerMailbox: st.eventsPerMailbox, MaxMailboxes: st.eventsMailboxes}, logger)
	} else {
		logger.Info("webmail: avisos en tiempo real desactivados (WEBMAIL_EVENTS_MAX_MAILBOXES=0)")
	}

	svc, err := app.New(app.Deps{
		Auth:        authClient,
		Sessions:    redisadapter.NewSessionStore(rdb, st.cellCode, st.sessions.Max),
		Mail:        store,
		Sender:      sender,
		Directory:   directory,
		Vacations:   directory,
		AddressBook: directory,
		Signatures:  directory,
		Filters:     directory,
		Passwords:   directory,
		Scheduled:   directory,
		Contacts:    dav,
		Calendar:    dav,
		LargeFiles:  largeFiles,
		Scheduling:  dav,
		Invitations: rfc5322.New(),
		Watcher:     watcher,
		Ledger:      redisadapter.NewSendLedger(rdb, st.cellCode),
		Composer:    rfc5322.New(),
		Sanitizer:   htmlsafe.New(),
		Scanner:     scanner,
		PartURL:     handler.PartURL,
		Logger:      logger,
		Config: app.Config{
			CellCode: st.cellCode, Sessions: st.sessions, Limits: st.limits,
			MaxBodyPartBytes: st.maxBodyPartBytes, MaxAttachmentBytes: st.maxAttachmentBytes,
			SendTimeout: transferTimeout, MaxScheduledDays: st.scheduledMaxDays,
			ScheduledPollInterval: st.scheduledPoll, ScheduledBatch: st.scheduledBatch,
			MaxImportBytes:  st.maxImportBytes,
			MaxReminderDays: st.remindersMaxDays, ReminderPollInterval: st.remindersPoll, ReminderBatch: st.remindersBatch,
		},

		// La baja en un clic es la unica salida del webmail a servidores de terceros: el cliente
		// solo conecta con direcciones publicas y sin redirecciones.
		Unsubscriber: unsubscribe.New(unsubscribeTimeout),
		Reminders:    directory,
		QuickReplies: directory,
	})
	if err != nil {
		log.Fatalf("webmail: %v", err)
	}
	h, err := handler.NewHandler(svc, handler.Config{
		CookieSecure: st.cookieSecure, SessionIdle: st.sessions.Idle, SessionMax: st.sessions.Max,
		AllowedOrigins: st.origins, MaxMessageBytes: st.limits.MaxMessageBytes,
		OperationTimeout: operationTimeout, TransferTimeout: transferTimeout,
		MaxLargeFileBytes: largeFileCeiling, ComposeConcurrency: st.composeConcurrency,
	}, logger)
	if err != nil {
		log.Fatalf("webmail: %v", err)
	}

	// Revocacion por eventos del directorio. Sin NATS el servicio sirve igual, pero un
	// cambio de contrasena no cierra las sesiones abiertas hasta su inactividad o su vida
	// maxima: se avisa.
	var auditBus natsadapter.Publisher
	if bus, err := events.NewBus(cfg.NATS.URL, logger); err != nil {
		logger.Warn("webmail: NATS no disponible; sin revocacion de sesiones por eventos de buzon", zap.Error(err))
	} else {
		defer bus.Close()
		go natsadapter.NewConsumer(bus, svc, logger).Run(ctx)
		auditBus = bus
	}

	// Asistente: sin NATS cada uso se cuenta como uso sin apunte de auditoria
	// (webmail_assistant_audit_failures_total).
	assistant, err := newAssistant(assistantCfg, app.AssistantDeps{
		Settings: directory,
		Quota:    redisadapter.NewAssistantQuota(rdb, st.cellCode),
		Audit:    natsadapter.NewAssistantAudit(auditBus),
		Metrics:  promadapter.NewAssistantMetrics(prometheus.DefaultRegisterer),
		Source:   svc,
	}, logger)
	if err != nil {
		log.Fatalf("webmail: %v", err)
	}
	h.SetAssistant(assistant)

	// Envio programado: reclama las filas vencidas de la celda (cada replica lo hace; el arriendo de
	// mail-directory evita que dos envien la misma) y termina con ctx.
	go svc.RunScheduledSends(ctx)
	// Recordatorios: el mismo reparto por arriendo, con su propio intervalo y lote.
	go svc.RunReminders(ctx)

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	// Unica entrada: el gateway. Sin su token no se acepta X-Real-IP, que alimenta el
	// freno de fuerza bruta de mail-auth.
	r.Use(middleware.RequireGatewayToken)
	r.Use(middleware.Logger(logger))
	r.Use(middleware.NewRateLimiter(rateLimitPerMinute, time.Minute).Limit)
	r.Mount("/", h.Routes())

	srv := server.New(st.port, r, logger)
	runErr := srv.Run()
	cancel()
	if runErr != nil {
		logger.Fatal("server error", zap.Error(runErr))
	}
}

// loadSettings lee y valida la configuracion. Todo valor ilegible es un error de
// despliegue: no se cae a un valor por defecto en silencio.
func loadSettings() (settings, error) {
	devRelaxations := config.DeclaredDevelopmentOrTest()
	var st settings
	var err error
	if st.port, err = config.EnvInt("WEBMAIL_PORT", defaultPort, 1, config.MaxPort); err != nil {
		return st, err
	}
	if st.cellCode = strings.TrimSpace(os.Getenv("CELL_CODE")); !domain.ValidCellCode(st.cellCode) {
		return st, fmt.Errorf("CELL_CODE %q no es un codigo de celda: el webmail es un servicio de celda y la celda va en cada token de sesion", st.cellCode)
	}
	if st.mailAuthURL, err = required("MAIL_AUTH_URL"); err != nil {
		return st, err
	}
	if st.imapAddr, err = required("WEBMAIL_IMAP_ADDR"); err != nil {
		return st, err
	}
	if st.smtpAddr, err = required("WEBMAIL_SMTP_ADDR"); err != nil {
		return st, err
	}
	st.imapTLS = envString("WEBMAIL_IMAP_TLS", imapadapter.TLSImplicit)
	st.smtpTLS = envString("WEBMAIL_SMTP_TLS", smtpadapter.TLSStartTLS)
	st.tlsServerName = envString("WEBMAIL_TLS_SERVER_NAME", os.Getenv("MAIL_HOSTNAME"))
	if st.tlsServerName == "" {
		if st.tlsServerName, _, err = net.SplitHostPort(st.imapAddr); err != nil {
			return st, fmt.Errorf("WEBMAIL_IMAP_ADDR invalido: %v", err)
		}
	}
	st.tlsCAFile = os.Getenv("WEBMAIL_TLS_CA_FILE")
	if st.tlsInsecure, err = envBool("WEBMAIL_TLS_INSECURE_SKIP_VERIFY", false); err != nil {
		return st, err
	}
	if !devRelaxations && (st.tlsInsecure || st.imapTLS == imapadapter.TLSNone) {
		return st, errors.New("el webmail exige TLS verificado con Dovecot, Postfix y mail-auth: WEBMAIL_TLS_INSECURE_SKIP_VERIFY=true y WEBMAIL_IMAP_TLS=none solo valen con ENVIRONMENT development o test")
	}

	if st.masterUser, err = required("WEBMAIL_MASTER_USER"); err != nil {
		return st, err
	}
	if !strings.Contains(st.masterUser, "@") || strings.ContainsAny(st.masterUser, "* \t\r\n\"") {
		return st, errors.New("WEBMAIL_MASTER_USER debe ser <DOVECOT_MASTER_USER>@platform.local")
	}
	st.masterPassword = os.Getenv("WEBMAIL_MASTER_PASSWORD")
	if len(st.masterPassword) < minMasterPasswordLen {
		return st, fmt.Errorf("WEBMAIL_MASTER_PASSWORD debe llegar del almacen de secretos y tener al menos %d caracteres", minMasterPasswordLen)
	}

	if st.sessions.Idle, err = config.EnvDuration("WEBMAIL_SESSION_IDLE", defaultSessionIdle, time.Minute, maxSessionIdle); err != nil {
		return st, err
	}
	if st.sessions.Max, err = config.EnvDuration("WEBMAIL_SESSION_MAX", defaultSessionMax, time.Minute, maxSessionMax); err != nil {
		return st, err
	}
	if err := st.sessions.Validate(); err != nil {
		return st, err
	}
	if st.eventsPerMailbox, err = config.EnvInt("WEBMAIL_EVENTS_MAX_PER_MAILBOX", defaultEventsMaxPerMailbox, 1, maxEventsMaxPerMailbox); err != nil {
		return st, err
	}
	if st.eventsMailboxes, err = config.EnvInt("WEBMAIL_EVENTS_MAX_MAILBOXES", defaultEventsMaxMailboxes, 0, maxEventsMaxMailboxes); err != nil {
		return st, err
	}
	if st.limits.MaxRecipients, err = config.EnvInt("WEBMAIL_MAX_RECIPIENTS", defaultMaxRecipients, 1, postfixRecipientLimit); err != nil {
		return st, err
	}
	if st.limits.MaxMessageBytes, err = envBytes("WEBMAIL_MAX_MESSAGE_BYTES", defaultMaxMessageBytes); err != nil {
		return st, err
	}
	if err := st.limits.Validate(); err != nil {
		return st, err
	}
	if st.maxBodyPartBytes, err = envBytes("WEBMAIL_MAX_BODY_PART_BYTES", defaultMaxBodyPartBytes); err != nil {
		return st, err
	}
	if st.maxAttachmentBytes, err = envBytes("WEBMAIL_MAX_ATTACHMENT_BYTES", defaultMaxAttachmentBytes); err != nil {
		return st, err
	}

	st.clamdAddr = strings.TrimSpace(os.Getenv("WEBMAIL_CLAMD_ADDR"))
	allowUnscanned, err := envBool("WEBMAIL_ALLOW_UNSCANNED_ATTACHMENTS", false)
	if err != nil {
		return st, err
	}
	if st.clamdAddr == "" && (!allowUnscanned || !devRelaxations) {
		return st, errors.New("WEBMAIL_CLAMD_ADDR es obligatorio: ClamAV analiza cada adjunto antes de enviarlo o guardarlo (WEBMAIL_ALLOW_UNSCANNED_ATTACHMENTS=true solo vale con ENVIRONMENT development o test)")
	}

	// Secure por defecto; se apaga a proposito solo en desarrollo local sin TLS, con la
	// misma variable que la cookie de sesion de la plataforma.
	if st.cookieSecure, err = envBool("AUTH_COOKIE_SECURE", true); err != nil {
		return st, err
	}
	st.origins = append(strings.Split(os.Getenv("CORS_ALLOWED_ORIGINS"), ","), os.Getenv("API_ORIGIN"))
	st.heloName = envString("MAIL_HOSTNAME", "localhost")

	// Los remitentes del buzon los resuelve mail-directory con la regla de Postfix.
	if st.mailDirectoryURL, err = config.RequiredServiceURL("MAIL_DIRECTORY_URL"); err != nil {
		return st, err
	}
	// La libreta personal y el calendario los sirve mail-dav en la base de la empresa.
	if st.mailDavURL, err = config.RequiredServiceURL("MAIL_DAV_URL"); err != nil {
		return st, err
	}
	// Los ficheros grandes por enlace los guarda mail-files; sin el la funcion queda apagada.
	if st.mailFilesURL, err = config.ServiceURL("MAIL_FILES_URL", ""); err != nil {
		return st, err
	}
	if st.scheduledPoll, err = config.EnvDuration("WEBMAIL_SCHEDULED_POLL_INTERVAL", defaultScheduledPollInterval, minScheduledPollInterval, maxScheduledPollInterval); err != nil {
		return st, err
	}
	if st.scheduledBatch, err = config.EnvInt("WEBMAIL_SCHEDULED_BATCH", defaultScheduledBatch, 1, maxScheduledBatch); err != nil {
		return st, err
	}
	if st.composeConcurrency, err = config.EnvInt("WEBMAIL_COMPOSE_CONCURRENCY", defaultComposeConcurrency, 1, maxComposeConcurrency); err != nil {
		return st, err
	}
	if st.scheduledMaxDays, err = config.EnvInt("WEBMAIL_SCHEDULED_MAX_DAYS", defaultScheduledMaxDays, 1, maxScheduledMaxDays); err != nil {
		return st, err
	}
	if st.remindersPoll, err = config.EnvDuration("WEBMAIL_REMINDERS_POLL_INTERVAL", defaultRemindersPollInterval, minRemindersPollInterval, maxRemindersPollInterval); err != nil {
		return st, err
	}
	if st.remindersBatch, err = config.EnvInt("WEBMAIL_REMINDERS_BATCH", defaultRemindersBatch, 1, maxRemindersBatch); err != nil {
		return st, err
	}
	if st.remindersMaxDays, err = config.EnvInt("WEBMAIL_REMINDERS_MAX_DAYS", defaultRemindersMaxDays, 1, maxRemindersMaxDays); err != nil {
		return st, err
	}
	importBytes, err := config.EnvInt("WEBMAIL_MAX_IMPORT_BYTES", defaultMaxImportBytes, 1, maxMaxImportBytes)
	if err != nil {
		return st, err
	}
	st.maxImportBytes = int64(importBytes)
	if st.internalToken, err = middleware.InternalGatewayToken(); err != nil {
		return st, fmt.Errorf("%w (sin el mail-directory rechaza la consulta de remitentes)", err)
	}
	return st, nil
}

// tlsConfig verifica contra las raices del sistema mas, si se indica, un fichero PEM
// propio (la CA interna que firma el certificado de mail-auth, por ejemplo).
func tlsConfig(serverName, caFile string, insecure bool) (*tls.Config, error) {
	if serverName == "" {
		return nil, errors.New("falta el nombre de servidor TLS")
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: serverName, InsecureSkipVerify: insecure}
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("leer WEBMAIL_TLS_CA_FILE: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("WEBMAIL_TLS_CA_FILE no contiene certificados PEM")
		}
		cfg.RootCAs = pool
	}
	return cfg, nil
}

func required(key string) (string, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return "", fmt.Errorf("%s es obligatorio", key)
	}
	return v, nil
}

func envString(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// envBytes lee un tope en bytes, que ningun mensaje de la celda puede superar y que clamd
// analiza entero: un adjunto mayor que StreamMaxLength o MaxFileSize no tendria veredicto
// (el analisis falla cerrado y el envio se corta con 503 SCAN_UNAVAILABLE).
func envBytes(key string, def int) (int64, error) {
	n, err := config.EnvInt(key, def, 1, postfixMessageSizeLimit)
	return int64(n), err
}

func envBool(key string, fallback bool) (bool, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s debe ser true o false", key)
	}
	return b, nil
}
