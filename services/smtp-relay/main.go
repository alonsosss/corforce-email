// smtp-relay recibe correo de las integraciones de las empresas por SMTP autenticado y lo entrega
// a transactional, que lo envia por Amazon SES con las mismas reglas que su API (ADR 0013). No pasa
// por los motores de la celda: Postfix sigue siendo solo el correo corporativo.
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/apikey"
	"github.com/alonsosss/corforce-email/pkg/config"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/rawmail"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/server"
	"github.com/alonsosss/corforce-email/services/smtp-relay/internal/adapters/accesscontrol"
	clamavadapter "github.com/alonsosss/corforce-email/services/smtp-relay/internal/adapters/clamav"
	mimeadapter "github.com/alonsosss/corforce-email/services/smtp-relay/internal/adapters/mime"
	promadapter "github.com/alonsosss/corforce-email/services/smtp-relay/internal/adapters/prometheus"
	redisadapter "github.com/alonsosss/corforce-email/services/smtp-relay/internal/adapters/redis"
	smtpadapter "github.com/alonsosss/corforce-email/services/smtp-relay/internal/adapters/smtp"
	"github.com/alonsosss/corforce-email/services/smtp-relay/internal/adapters/tlscert"
	"github.com/alonsosss/corforce-email/services/smtp-relay/internal/adapters/transactionalcli"
	"github.com/alonsosss/corforce-email/services/smtp-relay/internal/app"
	"github.com/alonsosss/corforce-email/services/smtp-relay/internal/ports"
	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

const (
	defaultPort              = 8060
	defaultStartTLSPort      = 2525
	defaultTLSPort           = 2465
	defaultMaxMessageBytes   = 10 << 20
	minMessageBytes          = 64 << 10
	defaultMaxRecipients     = 50
	maxRecipients            = 50
	defaultMaxConnections    = 200
	defaultConcurrentData    = 4
	defaultConnectionsPerIP  = 60
	defaultMessagesPerKey    = 300
	defaultMessagesPerIP     = 600
	defaultAuthMaxFailures   = 10
	defaultAuthMaxFailuresIP = 30
	defaultAuthWindow        = 15 * time.Minute
	defaultAuthLock          = 30 * time.Minute
	defaultReadTimeout       = 2 * time.Minute
	defaultDeliverTimeout    = 2 * time.Minute
	clamdTimeout             = 60 * time.Second
	defaultAccessControlURL  = "http://access-control:8002"
)

// settings son los valores del relay, validados antes de abrir ningun puerto.
type settings struct {
	port, startTLSPort, tlsPort int
	hostname                    string
	certFile, keyFile           string
	clamdAddr                   string
	transactionalURL            string
	accessControlURL            string
	cacheTTL                    time.Duration
	relay                       app.Config
	smtp                        smtpadapter.Options
	limits                      redisadapter.LimitsConfig
	throttle                    redisadapter.ThrottleConfig
}

func envInts(dst []*int, specs []struct {
	key         string
	def, lo, hi int
}) error {
	for i, s := range specs {
		v, err := config.EnvInt(s.key, s.def, s.lo, s.hi)
		if err != nil {
			return err
		}
		*dst[i] = v
	}
	return nil
}

func loadSettings() (settings, error) {
	var st settings
	var authFailures, authFailuresIP int
	if err := envInts(
		[]*int{&st.port, &st.startTLSPort, &st.tlsPort, &st.relay.MaxMessageBytes, &st.relay.MaxRecipients,
			&st.smtp.MaxConnections, &st.smtp.MaxConcurrentData, &st.limits.ConnectionsPerIP, &st.limits.MessagesPerKey,
			&st.limits.MessagesPerIP, &authFailures, &authFailuresIP},
		[]struct {
			key         string
			def, lo, hi int
		}{
			{"SMTP_RELAY_PORT", defaultPort, 1, config.MaxPort},
			{"SMTP_RELAY_STARTTLS_PORT", defaultStartTLSPort, 1, config.MaxPort},
			{"SMTP_RELAY_TLS_PORT", defaultTLSPort, 1, config.MaxPort},
			// Nunca por encima del tope de SES v2 con adjuntos.
			{"SMTP_RELAY_MAX_MESSAGE_BYTES", defaultMaxMessageBytes, minMessageBytes, rawmail.MaxSESBytes},
			// El de transactional por peticion.
			{"SMTP_RELAY_MAX_RECIPIENTS", defaultMaxRecipients, 1, maxRecipients},
			{"SMTP_RELAY_MAX_CONNECTIONS", defaultMaxConnections, 1, 10000},
			{"SMTP_RELAY_MAX_CONCURRENT_DATA", defaultConcurrentData, 1, 256},
			{"SMTP_RELAY_CONNECTIONS_PER_MIN_PER_IP", defaultConnectionsPerIP, 1, 100000},
			{"SMTP_RELAY_MESSAGES_PER_MIN_PER_KEY", defaultMessagesPerKey, 1, 100000},
			{"SMTP_RELAY_MESSAGES_PER_MIN_PER_IP", defaultMessagesPerIP, 1, 100000},
			{"SMTP_RELAY_AUTH_MAX_FAILURES", defaultAuthMaxFailures, 1, 1000},
			{"SMTP_RELAY_AUTH_MAX_FAILURES_PER_IP", defaultAuthMaxFailuresIP, 1, 10000},
		}); err != nil {
		return st, err
	}
	if st.startTLSPort == st.tlsPort || st.startTLSPort == st.port || st.tlsPort == st.port {
		return st, fmt.Errorf("SMTP_RELAY_PORT, SMTP_RELAY_STARTTLS_PORT y SMTP_RELAY_TLS_PORT deben ser distintos")
	}
	if authFailuresIP < authFailures {
		return st, fmt.Errorf("SMTP_RELAY_AUTH_MAX_FAILURES_PER_IP=%d no puede ser menor que SMTP_RELAY_AUTH_MAX_FAILURES=%d", authFailuresIP, authFailures)
	}
	st.throttle.MaxFailures, st.throttle.MaxFailuresPerIP = int64(authFailures), int64(authFailuresIP)
	var err error
	durations := []struct {
		dst         *time.Duration
		key         string
		def, lo, hi time.Duration
	}{
		{&st.throttle.Window, "SMTP_RELAY_AUTH_WINDOW", defaultAuthWindow, time.Minute, 24 * time.Hour},
		{&st.throttle.LockTTL, "SMTP_RELAY_AUTH_LOCK", defaultAuthLock, time.Minute, 24 * time.Hour},
		{&st.smtp.ReadTimeout, "SMTP_RELAY_READ_TIMEOUT", defaultReadTimeout, 10 * time.Second, 10 * time.Minute},
		{&st.smtp.DeliverTimeout, "SMTP_RELAY_DELIVER_TIMEOUT", defaultDeliverTimeout, 10 * time.Second, 10 * time.Minute},
		{&st.cacheTTL, "API_KEY_CACHE_TTL", apikey.DefaultCacheTTL, time.Second, apikey.MaxCacheTTL},
	}
	for _, d := range durations {
		if *d.dst, err = config.EnvDuration(d.key, d.def, d.lo, d.hi); err != nil {
			return st, err
		}
	}
	st.smtp.WriteTimeout = st.smtp.ReadTimeout
	st.smtp.DataWait = st.smtp.ReadTimeout
	st.smtp.MaxMessageBytes, st.smtp.MaxRecipients = st.relay.MaxMessageBytes, st.relay.MaxRecipients

	st.hostname = strings.TrimSpace(os.Getenv("SMTP_RELAY_HOSTNAME"))
	if !config.ValidHost(st.hostname) {
		return st, fmt.Errorf("SMTP_RELAY_HOSTNAME=%q debe ser el nombre publico del relay (el del certificado)", st.hostname)
	}
	st.smtp.Domain = st.hostname
	st.certFile = strings.TrimSpace(os.Getenv("SMTP_RELAY_TLS_CERT_FILE"))
	st.keyFile = strings.TrimSpace(os.Getenv("SMTP_RELAY_TLS_KEY_FILE"))
	if st.certFile == "" || st.keyFile == "" {
		return st, fmt.Errorf("SMTP_RELAY_TLS_CERT_FILE y SMTP_RELAY_TLS_KEY_FILE son obligatorias: sin TLS no se admite AUTH")
	}
	st.clamdAddr = strings.TrimSpace(os.Getenv("SMTP_RELAY_CLAMD_ADDR"))
	if st.clamdAddr == "" {
		if !config.DeclaredDevelopmentOrTest() {
			return st, fmt.Errorf("SMTP_RELAY_CLAMD_ADDR es obligatoria: ningun adjunto sale sin analizar")
		}
	} else if host, port, err := net.SplitHostPort(st.clamdAddr); err != nil || !config.ValidHost(host) {
		return st, fmt.Errorf("SMTP_RELAY_CLAMD_ADDR=%q debe ser host:puerto", st.clamdAddr)
	} else if _, err := config.ParsePort(port); err != nil {
		return st, fmt.Errorf("SMTP_RELAY_CLAMD_ADDR: %w", err)
	}
	if st.transactionalURL, err = config.RequiredServiceURL("TRANSACTIONAL_URL"); err != nil {
		return st, err
	}
	if st.accessControlURL, err = config.ServiceURL("ACCESS_CONTROL_URL", defaultAccessControlURL); err != nil {
		return st, err
	}
	return st, nil
}

func newRedis(logger *zap.Logger) (*redis.Client, error) {
	rc, err := config.LoadRedis()
	if err != nil {
		return nil, err
	}
	tlsCfg, err := rc.TLSConfig()
	if err != nil {
		return nil, err
	}
	rdb := redis.NewClient(&redis.Options{
		Addr: rc.Addr(), Password: rc.Password, TLSConfig: tlsCfg,
		DialTimeout: time.Second, ReadTimeout: 250 * time.Millisecond, WriteTimeout: 250 * time.Millisecond,
		PoolTimeout: 250 * time.Millisecond, ContextTimeoutEnabled: true,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		logger.Warn("smtp-relay: Redis no disponible al arrancar; cupos en memoria y sin freno compartido hasta que vuelva", zap.Error(err))
	}
	return rdb, nil
}

func main() {
	logger, _ := zap.NewProduction()
	defer logger.Sync()
	response.SetUnexpectedLogger(logger)

	st, err := loadSettings()
	if err != nil {
		log.Fatal(err)
	}
	token, err := middleware.InternalGatewayToken()
	if err != nil {
		log.Fatal(err)
	}
	certs, err := tlscert.New(st.certFile, st.keyFile, logger)
	if err != nil {
		log.Fatal(err)
	}
	promadapter.RegisterCertificateExpiry(certs.NotAfter)
	rdb, err := newRedis(logger)
	if err != nil {
		log.Fatalf("redis: %v", err)
	}
	defer rdb.Close()

	var scanner ports.Scanner
	if st.clamdAddr != "" {
		scanner = clamavadapter.New(st.clamdAddr, clamdTimeout)
	} else {
		logger.Warn("smtp-relay: sin SMTP_RELAY_CLAMD_ADDR; los mensajes con adjuntos se rechazan con 451")
	}
	relay := app.New(app.Deps{
		Auth:      accesscontrol.New(apikey.NewResolver(st.accessControlURL, token, st.cacheTTL, apikey.NewRedisRevocations(rdb))),
		Throttle:  redisadapter.NewThrottle(rdb, st.throttle, logger),
		Limits:    redisadapter.NewLimits(middleware.NewRedisRateLimitStore(rdb), st.limits, logger),
		Inspector: mimeadapter.New(st.relay.MaxMessageBytes),
		Scanner:   scanner,
		Submitter: transactionalcli.New(st.transactionalURL, token, st.smtp.DeliverTimeout),
		Metrics:   promadapter.New(),
		Config:    st.relay,
		Logger:    logger,
	})
	smtpServer := smtpadapter.NewServer(relay, certs.Config(), st.smtp, logger)

	startTLS, err := net.Listen("tcp", fmt.Sprintf(":%d", st.startTLSPort))
	if err != nil {
		log.Fatalf("SMTP_RELAY_STARTTLS_PORT: %v", err)
	}
	implicitTLS, err := net.Listen("tcp", fmt.Sprintf(":%d", st.tlsPort))
	if err != nil {
		log.Fatalf("SMTP_RELAY_TLS_PORT: %v", err)
	}
	go serveSMTP(logger, "starttls", func() error { return smtpServer.ServeStartTLS(startTLS) })
	go serveSMTP(logger, "tls", func() error { return smtpServer.ServeTLS(implicitTLS) })
	logger.Info("smtp-relay escuchando",
		zap.Int("starttls_port", st.startTLSPort), zap.Int("tls_port", st.tlsPort), zap.String("hostname", st.hostname),
		zap.Int("max_message_bytes", st.relay.MaxMessageBytes), zap.Int("max_recipients", st.relay.MaxRecipients))

	// El puerto HTTP es solo de operativa: salud y metricas (pkg/observability). No hay API: el
	// gateway no enruta nada a este servicio.
	srv := server.New(st.port, chi.NewRouter(), logger)
	if err := srv.Run(); err != nil {
		logger.Error("server error", zap.Error(err))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	smtpServer.Shutdown(ctx)
}

func serveSMTP(logger *zap.Logger, name string, serve func() error) {
	if err := serve(); err != nil {
		logger.Fatal("smtp-relay: el servidor SMTP se detuvo", zap.String("listener", name), zap.Error(err))
	}
}
