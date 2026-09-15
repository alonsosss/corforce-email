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
	"github.com/alonsosss/corforce-email/services/webmail/internal/adapters/maildirectorycli"
	natsadapter "github.com/alonsosss/corforce-email/services/webmail/internal/adapters/nats"
	redisadapter "github.com/alonsosss/corforce-email/services/webmail/internal/adapters/redis"
	"github.com/alonsosss/corforce-email/services/webmail/internal/adapters/rfc5322"
	smtpadapter "github.com/alonsosss/corforce-email/services/webmail/internal/adapters/smtp"
	"github.com/alonsosss/corforce-email/services/webmail/internal/app"
	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"github.com/alonsosss/corforce-email/services/webmail/internal/ports"
	"github.com/go-chi/chi/v5"
	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

const (
	defaultPort               = 8044
	defaultSessionIdle        = 30 * time.Minute
	defaultSessionMax         = 12 * time.Hour
	defaultMaxRecipients      = 100
	defaultMaxMessageBytes    = 25 << 20
	defaultMaxBodyPartBytes   = 2 << 20
	defaultMaxAttachmentBytes = 50 << 20

	// Topes de los motores de la celda, por donde entra y sale todo mensaje: el
	// message_size_limit de deploy/mail/postfix/conf/main.cf (ningun mensaje, ni por tanto
	// ninguna de sus partes, lo supera) y el smtpd_recipient_limit de Postfix, que main.cf
	// deja en su valor por defecto.
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
)

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
	internalToken      string
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
	var scanner ports.VirusScanner
	if st.clamdAddr != "" {
		scanner = clamav.New(st.clamdAddr, clamdTimeout)
	} else {
		logger.Warn("webmail: WEBMAIL_ALLOW_UNSCANNED_ATTACHMENTS=true, los adjuntos NO se analizan con ClamAV (solo desarrollo)")
	}

	svc, err := app.New(app.Deps{
		Auth:      authClient,
		Sessions:  redisadapter.NewSessionStore(rdb, st.cellCode, st.sessions.Max),
		Mail:      store,
		Sender:    sender,
		Directory: directory,
		Ledger:    redisadapter.NewSendLedger(rdb, st.cellCode),
		Composer:  rfc5322.New(),
		Sanitizer: htmlsafe.New(),
		Scanner:   scanner,
		PartURL:   handler.PartURL,
		Logger:    logger,
		Config: app.Config{
			CellCode: st.cellCode, Sessions: st.sessions, Limits: st.limits,
			MaxBodyPartBytes: st.maxBodyPartBytes, MaxAttachmentBytes: st.maxAttachmentBytes,
			SendTimeout: transferTimeout,
		},
	})
	if err != nil {
		log.Fatalf("webmail: %v", err)
	}
	h, err := handler.NewHandler(svc, handler.Config{
		CookieSecure: st.cookieSecure, SessionIdle: st.sessions.Idle, SessionMax: st.sessions.Max,
		AllowedOrigins: st.origins, MaxMessageBytes: st.limits.MaxMessageBytes,
		OperationTimeout: operationTimeout, TransferTimeout: transferTimeout,
	}, logger)
	if err != nil {
		log.Fatalf("webmail: %v", err)
	}

	// Revocacion por eventos del directorio. Sin NATS el servicio sirve igual, pero un
	// cambio de contrasena no cierra las sesiones abiertas hasta su inactividad o su vida
	// maxima: se avisa.
	if bus, err := events.NewBus(cfg.NATS.URL, logger); err != nil {
		logger.Warn("webmail: NATS no disponible; sin revocacion de sesiones por eventos de buzon", zap.Error(err))
	} else {
		defer bus.Close()
		go natsadapter.NewConsumer(bus, svc, logger).Run(ctx)
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	// Unica entrada: el gateway. Sin su token no se acepta X-Real-IP, que alimenta el
	// freno de fuerza bruta de mail-auth.
	r.Use(middleware.RequireGatewayToken)
	r.Use(middleware.Logger(logger))
	r.Use(middleware.NewRateLimiter(rateLimitPerMinute, time.Minute).Limit)
	r.Mount("/", h.Routes())

	srv := server.New(st.port, r, logger)
	if err := srv.Run(); err != nil {
		logger.Fatal("server error", zap.Error(err))
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
	if st.mailDirectoryURL, err = required("MAIL_DIRECTORY_URL"); err != nil {
		return st, err
	}
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

// envBytes lee un tope en bytes, que ningun mensaje de la celda puede superar.
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
