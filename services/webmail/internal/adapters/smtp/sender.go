// Package smtp entrega los mensajes del webmail por el submission de Postfix de la celda.
//
// La sesion SMTP se autentica como el buzon (usuario*maestro de Dovecot, que es quien
// resuelve el SASL de Postfix): el nombre SASL que ve Postfix es el del buzon real, y
// reject_authenticated_sender_login_mismatch con smtpd_sender_login_maps decide si el
// remitente le pertenece. El webmail no puede saltarse esa comprobacion.
package smtp

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	netsmtp "net/smtp"
	"net/textproto"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"go.uber.org/zap"
)

// Modos de TLS del submission. No hay modo sin TLS: Postfix no ofrece AUTH antes de
// STARTTLS (smtpd_tls_auth_only) y la credencial maestra no viaja en claro.
const (
	TLSStartTLS = "starttls"
	TLSImplicit = "implicit"
)

type Config struct {
	Addr           string
	TLSMode        string
	TLSConfig      *tls.Config
	MasterUser     string
	MasterPassword string
	HeloName       string
	Timeout        time.Duration
}

// Sender implementa ports.Sender.
type Sender struct {
	cfg    Config
	logger *zap.Logger
}

func NewSender(cfg Config, logger *zap.Logger) (*Sender, error) {
	if cfg.Addr == "" || cfg.MasterUser == "" || cfg.MasterPassword == "" {
		return nil, errors.New("smtp: faltan la direccion del submission o la credencial maestra")
	}
	if cfg.TLSMode != TLSStartTLS && cfg.TLSMode != TLSImplicit {
		return nil, fmt.Errorf("smtp: modo TLS invalido %q (starttls o implicit)", cfg.TLSMode)
	}
	if cfg.TLSConfig == nil || cfg.TLSConfig.ServerName == "" {
		return nil, errors.New("smtp: falta el nombre de servidor TLS")
	}
	if cfg.HeloName == "" {
		cfg.HeloName = "localhost"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 60 * time.Second
	}
	return &Sender{cfg: cfg, logger: logger}, nil
}

// Send entrega raw a los destinatarios con envelopeFrom como remitente del sobre.
func (s *Sender) Send(ctx context.Context, username, envelopeFrom string, recipients []string, raw []byte) error {
	if strings.ContainsAny(username, "*\r\n") {
		return fmt.Errorf("%w: nombre de buzon invalido", domain.ErrUnavailable)
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()

	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", s.cfg.Addr)
	if err != nil {
		return fmt.Errorf("%w: conectar con el submission: %v", domain.ErrUnavailable, err)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	serverName := s.cfg.TLSConfig.ServerName
	if s.cfg.TLSMode == TLSImplicit {
		tconn := tls.Client(conn, s.cfg.TLSConfig.Clone())
		if err := tconn.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return fmt.Errorf("%w: TLS con el submission: %v", domain.ErrUnavailable, err)
		}
		conn = tconn
	}
	c, err := netsmtp.NewClient(conn, serverName)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("%w: saludo del submission: %v", domain.ErrUnavailable, err)
	}
	defer c.Close()

	if err := c.Hello(s.cfg.HeloName); err != nil {
		return fmt.Errorf("%w: EHLO: %v", domain.ErrUnavailable, err)
	}
	if s.cfg.TLSMode == TLSStartTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return fmt.Errorf("%w: el submission no ofrece STARTTLS", domain.ErrUnavailable)
		}
		if err := c.StartTLS(s.cfg.TLSConfig.Clone()); err != nil {
			return fmt.Errorf("%w: STARTTLS: %v", domain.ErrUnavailable, err)
		}
	}
	// PlainAuth se niega a enviar la credencial sin TLS y con otro nombre de servidor.
	auth := netsmtp.PlainAuth("", username+"*"+s.cfg.MasterUser, s.cfg.MasterPassword, serverName)
	if err := c.Auth(auth); err != nil {
		s.logger.Error("webmail: el submission rechazo la autenticacion del buzon", zap.String("username", username), zap.Error(err))
		return fmt.Errorf("%w: autenticacion SMTP: %v", domain.ErrUnavailable, err)
	}
	if err := c.Mail(envelopeFrom); err != nil {
		return classify(err, "")
	}
	for _, rcpt := range recipients {
		if err := c.Rcpt(rcpt); err != nil {
			return classify(err, rcpt)
		}
	}
	w, err := c.Data()
	if err != nil {
		return classify(err, "")
	}
	if _, err := w.Write(raw); err != nil {
		return fmt.Errorf("%w: DATA: %v", domain.ErrUnavailable, err)
	}
	if err := w.Close(); err != nil {
		return classify(err, "")
	}
	if err := c.Quit(); err != nil {
		// El mensaje ya fue aceptado (250 tras el punto): un QUIT fallido no lo deshace.
		s.logger.Debug("webmail: QUIT del submission", zap.Error(err))
	}
	return nil
}

// classify traduce la respuesta de Postfix. Con smtpd_delay_reject=yes el rechazo del
// remitente llega en RCPT ("Sender address rejected: not owned by user ..."), por eso se
// mira el texto antes de atribuirlo al destinatario.
func classify(err error, rcpt string) error {
	var te *textproto.Error
	if !errors.As(err, &te) {
		return fmt.Errorf("%w: %v", domain.ErrUnavailable, err)
	}
	detail := fmt.Sprintf("%d %s", te.Code, te.Msg)
	switch {
	case te.Code >= 400 && te.Code < 500:
		return fmt.Errorf("%w: rechazo temporal: %s", domain.ErrUnavailable, detail)
	case te.Code == 552:
		return fmt.Errorf("%w: %s", domain.ErrMessageTooLarge, detail)
	case strings.Contains(strings.ToLower(te.Msg), "sender address rejected"):
		return fmt.Errorf("%w: %s", domain.ErrSenderNotAllowed, detail)
	case rcpt != "":
		return fmt.Errorf("%s: %w", detail, &domain.RecipientRejectedError{Address: rcpt})
	default:
		return fmt.Errorf("%w: %s", domain.ErrMessageRejected, detail)
	}
}
