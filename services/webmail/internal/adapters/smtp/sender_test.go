package smtp

import (
	"crypto/tls"
	"errors"
	"net/textproto"
	"testing"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"go.uber.org/zap"
)

func TestClassifyRespuestasDePostfix(t *testing.T) {
	cases := []struct {
		name string
		err  error
		rcpt string
		want error
	}{
		{"remitente ajeno en RCPT (smtpd_delay_reject)", &textproto.Error{Code: 553, Msg: "5.7.1 <director@empresa.pe>: Sender address rejected: not owned by user ana@empresa.pe"}, "luis@x.com", domain.ErrSenderNotAllowed},
		{"remitente ajeno en MAIL", &textproto.Error{Code: 553, Msg: "5.7.1 Sender address rejected: not logged in"}, "", domain.ErrSenderNotAllowed},
		{"rechazo temporal", &textproto.Error{Code: 451, Msg: "4.3.0 try again"}, "luis@x.com", domain.ErrUnavailable},
		{"demasiado grande", &textproto.Error{Code: 552, Msg: "5.3.4 Message size exceeds fixed limit"}, "", domain.ErrMessageTooLarge},
		{"rechazado por Rspamd", &textproto.Error{Code: 554, Msg: "5.7.1 Spam message rejected"}, "", domain.ErrMessageRejected},
		{"error de red", errors.New("broken pipe"), "", domain.ErrUnavailable},
	}
	for _, c := range cases {
		if got := classify(c.err, c.rcpt); !errors.Is(got, c.want) {
			t.Errorf("%s: %v", c.name, got)
		}
	}
	var rejected *domain.RecipientRejectedError
	got := classify(&textproto.Error{Code: 550, Msg: "5.1.1 <nadie@empresa.pe>: Recipient address rejected"}, "nadie@empresa.pe")
	if !errors.As(got, &rejected) || rejected.Address != "nadie@empresa.pe" {
		t.Fatalf("destinatario rechazado: %v", got)
	}
}

func TestNewSenderExigeTLSYCredencial(t *testing.T) {
	ok := Config{Addr: "postfix:587", TLSMode: TLSStartTLS, TLSConfig: &tls.Config{ServerName: "mail.x.com"}, MasterUser: "webmail@platform.local", MasterPassword: "p"}
	if _, err := NewSender(ok, zap.NewNop()); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Config){
		"sin TLS":        func(c *Config) { c.TLSMode = "none" },
		"sin nombre TLS": func(c *Config) { c.TLSConfig = &tls.Config{} },
		"sin maestro":    func(c *Config) { c.MasterUser = "" },
		"sin contrasena": func(c *Config) { c.MasterPassword = "" },
		"sin direccion":  func(c *Config) { c.Addr = "" },
	} {
		c := ok
		mutate(&c)
		if _, err := NewSender(c, zap.NewNop()); err == nil {
			t.Errorf("%s: se esperaba error", name)
		}
	}
}
