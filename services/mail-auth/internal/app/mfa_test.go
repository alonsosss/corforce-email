package app

import (
	"context"
	"testing"

	"github.com/alonsosss/corforce-email/services/mail-auth/internal/domain"
	"github.com/google/uuid"
)

func mailboxWithMFA() *domain.Mailbox {
	mb := activeMailbox()
	mb.MFAEnabled = true
	return mb
}

// Con verificacion en dos pasos, la contrasena principal por IMAP, POP3, SMTP, Sieve o DAV se
// saltaria el segundo factor: se rechaza sin alimentar el freno (es correcta) y sin dejar inicio.
func TestConVerificacionLaPrincipalSoloAbreElWebmail(t *testing.T) {
	for _, service := range []string{"imap", "pop3", "submission", "sieve", "dav"} {
		h := newHarness(mailboxWithMFA())
		v := h.uc.Authenticate(context.Background(), request("principal", service))
		if v.Result != domain.ResultMFAAppPasswordRequired || v.Result.Authorized() {
			t.Fatalf("%s: %s", service, v.Result)
		}
		if v.MailboxID != uuid.Nil || v.TenantID != uuid.Nil || v.MFARequired {
			t.Fatalf("%s: un rechazo no dice nada del buzon: %+v", service, v)
		}
		if h.throttle.failures != 0 || h.throttle.successes != 0 || len(h.repo.logins) != 0 {
			t.Fatalf("%s: freno %d/%d, inicios %d", service, h.throttle.failures, h.throttle.successes, len(h.repo.logins))
		}
		if h.metrics.last != domain.ResultMFAAppPasswordRequired {
			t.Fatalf("%s: metrica %s", service, h.metrics.last)
		}
	}
}

func TestConVerificacionLasContrasenasDeAplicacionSiguenValiendo(t *testing.T) {
	h := newHarness(mailboxWithMFA())
	app := domain.AppPassword{ID: uuid.New(), Name: "movil", PasswordHash: hashApp}
	h.repo.appPasswords, h.repo.appProtocol = []domain.AppPassword{app}, domain.ProtocolIMAP

	v := h.uc.Authenticate(context.Background(), request("movil", "imap"))
	if v.Result != domain.ResultOK || len(h.repo.logins) != 1 || h.repo.logins[0].AppPasswordID == nil {
		t.Fatalf("contrasena de aplicacion: %+v %+v", v, h.repo.logins)
	}
	if h.uc.Verify(context.Background(), request("otra", "imap")) != domain.ResultBadPassword || h.throttle.failures != 1 {
		t.Fatal("una contrasena incorrecta sigue siendo fuerza bruta")
	}
}

func TestElWebmailSabeQueFaltaElSegundoPaso(t *testing.T) {
	h := newHarness(mailboxWithMFA())
	v := h.uc.Authenticate(context.Background(), request("principal", "webmail"))
	if v.Result != domain.ResultOK || !v.MFARequired || v.MailboxID == uuid.Nil {
		t.Fatalf("webmail con verificacion: %+v", v)
	}
	h = newHarness(activeMailbox())
	if v := h.uc.Authenticate(context.Background(), request("principal", "webmail")); v.Result != domain.ResultOK || v.MFARequired {
		t.Fatalf("webmail sin verificacion: %+v", v)
	}
	if v := h.uc.Authenticate(context.Background(), request("principal", "imap")); v.Result != domain.ResultOK || v.MFARequired {
		t.Fatalf("imap sin verificacion: %+v", v)
	}
}

// Un buzon apagado sigue diciendo inactivo aunque tenga verificacion: el orden de las comprobaciones
// no cambia.
func TestConVerificacionUnBuzonApagadoSigueInactivo(t *testing.T) {
	mb := mailboxWithMFA()
	mb.Active = domain.MailboxInactive
	h := newHarness(mb)
	if got := h.uc.Verify(context.Background(), request("principal", "imap")); got != domain.ResultInactive {
		t.Fatalf("resultado %s", got)
	}
}
