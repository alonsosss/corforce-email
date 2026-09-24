// Package smtp es la cara SMTP del relay sobre github.com/emersion/go-smtp: dos puertos (STARTTLS
// obligatorio antes de AUTH, y TLS implicito), AUTH PLAIN y LOGIN solo sobre TLS, y cupos de
// conexiones y de mensajes en curso para que el relay no se quede sin memoria.
package smtp

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-sasl"
	gosmtp "github.com/emersion/go-smtp"
	"go.uber.org/zap"

	"github.com/alonsosss/corforce-email/services/smtp-relay/internal/app"
	"github.com/alonsosss/corforce-email/services/smtp-relay/internal/domain"
)

// Nombres de los dos puertos en las metricas.
const (
	ListenerStartTLS = "starttls"
	ListenerTLS      = "tls"
)

// Options son los limites de la cara SMTP, ya validados en main.
type Options struct {
	Domain          string
	MaxMessageBytes int
	MaxRecipients   int
	// MaxConnections acota las conexiones abiertas a la vez en cada puerto.
	MaxConnections int
	// MaxConcurrentData acota los mensajes que se leen y entregan a la vez: cada uno ocupa en
	// memoria varias veces su tamano (el DATA, su copia en JSON y el analisis).
	MaxConcurrentData int
	// DataWait es cuanto espera un DATA a que haya hueco antes de responder 451.
	DataWait     time.Duration
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	// DeliverTimeout acota el analisis y la entrega a transactional de un mensaje.
	DeliverTimeout time.Duration
}

// Server agrupa los dos servidores SMTP.
type Server struct {
	relay  *app.Relay
	opts   Options
	logger *zap.Logger
	data   chan struct{}
	tls    *tls.Config

	startTLS, implicit *gosmtp.Server
}

func NewServer(relay *app.Relay, tlsConfig *tls.Config, opts Options, logger *zap.Logger) *Server {
	s := &Server{relay: relay, opts: opts, logger: logger, data: make(chan struct{}, opts.MaxConcurrentData), tls: tlsConfig}
	s.startTLS = s.newSMTP()
	s.implicit = s.newSMTP()
	return s
}

func (s *Server) newSMTP() *gosmtp.Server {
	srv := gosmtp.NewServer(&backend{server: s})
	srv.Domain = s.opts.Domain
	srv.MaxMessageBytes = int64(s.opts.MaxMessageBytes)
	srv.MaxRecipients = s.opts.MaxRecipients
	srv.ReadTimeout = s.opts.ReadTimeout
	srv.WriteTimeout = s.opts.WriteTimeout
	srv.TLSConfig = s.tls
	// AUTH solo se anuncia y se admite dentro de TLS.
	srv.AllowInsecureAuth = false
	srv.ErrorLog = zapPrinter{s.logger}
	return srv
}

// ServeStartTLS atiende el puerto con STARTTLS sobre un listener TCP.
func (s *Server) ServeStartTLS(l net.Listener) error {
	return s.startTLS.Serve(s.admit(l, ListenerStartTLS, false))
}

// ServeTLS atiende el puerto de TLS implicito: la conexion es TLS desde el primer byte.
func (s *Server) ServeTLS(l net.Listener) error {
	return s.implicit.Serve(tls.NewListener(s.admit(l, ListenerTLS, true), s.tls))
}

// Shutdown cierra los dos servidores esperando a las sesiones en curso.
func (s *Server) Shutdown(ctx context.Context) {
	for _, srv := range []*gosmtp.Server{s.startTLS, s.implicit} {
		if err := srv.Shutdown(ctx); err != nil {
			s.logger.Warn("smtp-relay: cierre del servidor SMTP", zap.Error(err))
		}
	}
}

// admit envuelve el listener con el tope de conexiones abiertas y el cupo por IP, antes de
// cualquier saludo: una conexion rechazada no llega a costar una sesion.
func (s *Server) admit(l net.Listener, name string, implicitTLS bool) net.Listener {
	return &admitListener{Listener: l, server: s, name: name, implicitTLS: implicitTLS, slots: make(chan struct{}, s.opts.MaxConnections)}
}

type admitListener struct {
	net.Listener
	server      *Server
	name        string
	implicitTLS bool
	slots       chan struct{}
}

func (a *admitListener) Accept() (net.Conn, error) {
	for {
		c, err := a.Listener.Accept()
		if err != nil {
			return nil, err
		}
		ip := remoteIP(c.RemoteAddr())
		select {
		case a.slots <- struct{}{}:
		default:
			a.refuse(c)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err = a.server.relay.Admit(ctx, a.name, ip)
		cancel()
		if err != nil {
			<-a.slots
			a.refuse(c)
			continue
		}
		return &slotConn{Conn: c, release: func() { <-a.slots }}, nil
	}
}

// refuse cierra la conexion; en claro se despide antes con un 421, en TLS implicito no hay forma de
// hablar sin negociar antes.
func (a *admitListener) refuse(c net.Conn) {
	if !a.implicitTLS {
		_ = c.SetWriteDeadline(time.Now().Add(time.Second))
		_, _ = io.WriteString(c, "421 4.7.0 Too many connections, try again later\r\n")
	}
	_ = c.Close()
}

type slotConn struct {
	net.Conn
	release func()
	once    sync.Once
}

func (c *slotConn) Close() error {
	c.once.Do(c.release)
	return c.Conn.Close()
}

func remoteIP(addr net.Addr) string {
	if tcp, ok := addr.(*net.TCPAddr); ok {
		return tcp.IP.String()
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	return host
}

type backend struct {
	server *Server
}

func (b *backend) NewSession(c *gosmtp.Conn) (gosmtp.Session, error) {
	return &session{server: b.server, conn: c, ip: remoteIP(c.Conn().RemoteAddr())}, nil
}

type session struct {
	server *Server
	conn   *gosmtp.Conn
	ip     string
	cred   *domain.Credential
	env    domain.Envelope
}

func (s *session) tlsActive() bool {
	_, ok := s.conn.TLSConnectionState()
	return ok
}

func (s *session) AuthMechanisms() []string {
	if !s.tlsActive() {
		return nil
	}
	return []string{sasl.Plain, sasl.Login}
}

func (s *session) Auth(mech string) (sasl.Server, error) {
	if !s.tlsActive() {
		return nil, replies[domain.ErrAuthRequired.Reason]
	}
	authenticate := func(username, password string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cred, err := s.server.relay.Authenticate(ctx, username, password, s.ip)
		if err != nil {
			return reply(err)
		}
		s.cred = cred
		return nil
	}
	switch strings.ToUpper(mech) {
	case sasl.Plain:
		return sasl.NewPlainServer(func(identity, username, password string) error {
			if identity != "" && identity != username {
				return reply(domain.ErrAuthFailed)
			}
			return authenticate(username, password)
		}), nil
	case sasl.Login:
		return newLoginServer(authenticate), nil
	}
	return nil, gosmtp.ErrAuthUnknownMechanism
}

func (s *session) Mail(from string, opts *gosmtp.MailOptions) error {
	if opts != nil && opts.Size > int64(s.server.opts.MaxMessageBytes) {
		return replies[domain.ErrTooLarge.Reason]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	addr, err := s.server.relay.Mail(ctx, s.cred, s.ip, from)
	if err != nil {
		return reply(err)
	}
	s.env = domain.Envelope{From: addr}
	return nil
}

func (s *session) Rcpt(to string, _ *gosmtp.RcptOptions) error {
	if s.env.From == "" {
		return &gosmtp.SMTPError{Code: 503, EnhancedCode: gosmtp.EnhancedCode{5, 5, 1}, Message: "MAIL FROM first"}
	}
	recipients, err := s.server.relay.Rcpt(s.env.Recipients, to)
	s.env.Recipients = recipients
	return reply(err)
}

func (s *session) Data(r io.Reader) error {
	wait := time.NewTimer(s.server.opts.DataWait)
	defer wait.Stop()
	select {
	case s.server.data <- struct{}{}:
		defer func() { <-s.server.data }()
	case <-wait.C:
		_, _ = io.Copy(io.Discard, r)
		return replies[domain.ErrUpstream.Reason]
	}
	raw, err := io.ReadAll(r)
	if err != nil {
		var smtpErr *gosmtp.SMTPError
		if errors.As(err, &smtpErr) {
			return smtpErr
		}
		return replies[domain.ErrUpstream.Reason]
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.server.opts.DeliverTimeout)
	defer cancel()
	id, err := s.server.relay.Deliver(ctx, s.cred, s.env, raw)
	if err != nil {
		return reply(err)
	}
	s.server.logger.Info("smtp-relay: mensaje entregado a transactional",
		zap.String("message_id", id), zap.String("api_key_id", s.cred.KeyID), zap.Int("bytes", len(raw)))
	return nil
}

func (s *session) Reset() { s.env = domain.Envelope{} }

func (s *session) Logout() error { return nil }

// zapPrinter lleva los errores de conexion de go-smtp al registro.
type zapPrinter struct{ logger *zap.Logger }

func (p zapPrinter) Printf(format string, v ...interface{}) {
	p.logger.Sugar().Debugf("smtp: "+format, v...)
}

func (p zapPrinter) Println(v ...interface{}) {
	p.logger.Sugar().Debug(append([]interface{}{"smtp:"}, v...)...)
}
