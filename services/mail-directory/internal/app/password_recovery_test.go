package app

import (
	"context"
	"errors"
	"testing"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
)

const contrasenaNueva = "Una-Contrasena-Nueva-2030"

func TestRecuperarLaContrasenaConLosDosCodigos(t *testing.T) {
	h := newHarness()
	m := h.addMailbox(uuid.New(), "ana@acme.test", 0)
	codes := activar(t, h, "ana@acme.test")
	h.addAppPassword(m, nil)
	h.totp.codes["222222"] = 101
	h.events.subjects = nil

	res, err := h.uc.RecoverPasswordByUsername(context.Background(), "Ana@Acme.test", "222 222", codes[0], contrasenaNueva)
	if err != nil {
		t.Fatal(err)
	}
	if res.RecoveryRemaining != domain.RecoveryCodeCount-1 || res.AppPasswordsRevoked != 1 {
		t.Fatalf("desenlace: %+v", res)
	}
	if h.mailboxes.passwords[m.ID] == "" {
		t.Fatal("la contrasena nueva se guarda")
	}
	if h.appPasswords.deleted != 1 {
		t.Fatal("las contrasenas de aplicacion del buzon se borran")
	}
	if h.mfa.rows[m.ID] == nil || !m.MFAEnabled {
		t.Fatal("la verificacion en dos pasos sigue activa")
	}
	if h.published("mail.mailbox.credentials_changed") != 1 || h.published("mail.mailbox.password_recovered") != 1 || len(h.events.outside) != 0 {
		t.Fatalf("eventos: %v fuera de la transaccion: %v", h.events.subjects, h.events.outside)
	}
	last := h.events.credentials[len(h.events.credentials)-1]
	if last.credential != domain.CredentialPassword || len(last.changed) != 2 {
		t.Fatalf("el cambio sale como la credencial principal y cierra todas las sesiones: %+v", last)
	}

	if _, err := h.uc.RecoverPasswordByUsername(context.Background(), "ana@acme.test", "333333", codes[0], contrasenaNueva); !errors.Is(err, domain.ErrPasswordRecoveryRejected) {
		t.Fatalf("un codigo de recuperacion gastado no vale otra vez: %v", err)
	}
}

func TestLaRecuperacionNoDiceQueFallo(t *testing.T) {
	ctx := context.Background()
	casos := map[string]func(h *harness, m *domain.Mailbox, codes []string) (string, string, string){
		"buzon inexistente": func(_ *harness, _ *domain.Mailbox, codes []string) (string, string, string) {
			return "nadie@acme.test", "222222", codes[0]
		},
		"codigo TOTP incorrecto": func(_ *harness, _ *domain.Mailbox, codes []string) (string, string, string) {
			return "ana@acme.test", "999999", codes[0]
		},
		"codigo TOTP ya usado": func(_ *harness, _ *domain.Mailbox, codes []string) (string, string, string) {
			return "ana@acme.test", "111111", codes[0]
		},
		"codigo de recuperacion incorrecto": func(_ *harness, _ *domain.Mailbox, _ []string) (string, string, string) {
			return "ana@acme.test", "222222", "ZZZZZ-ZZZZZ"
		},
		"dos codigos TOTP": func(_ *harness, _ *domain.Mailbox, _ []string) (string, string, string) {
			return "ana@acme.test", "222222", "222222"
		},
		"sin codigo de recuperacion": func(_ *harness, _ *domain.Mailbox, _ []string) (string, string, string) {
			return "ana@acme.test", "222222", ""
		},
		"buzon inactivo": func(_ *harness, m *domain.Mailbox, codes []string) (string, string, string) {
			m.Active = 0
			return "ana@acme.test", "222222", codes[0]
		},
		"buzon sin verificacion": func(h *harness, m *domain.Mailbox, codes []string) (string, string, string) {
			delete(h.mfa.rows, m.ID)
			m.MFAEnabled = false
			return "ana@acme.test", "222222", codes[0]
		},
	}
	for nombre, preparar := range casos {
		t.Run(nombre, func(t *testing.T) {
			h := newHarness()
			m := h.addMailbox(uuid.New(), "ana@acme.test", 0)
			codes := activar(t, h, "ana@acme.test")
			h.addAppPassword(m, nil)
			h.totp.codes["222222"] = 101
			h.events.subjects = nil
			username, totp, recovery := preparar(h, m, codes)

			_, err := h.uc.RecoverPasswordByUsername(ctx, username, totp, recovery, contrasenaNueva)
			if !errors.Is(err, domain.ErrPasswordRecoveryRejected) {
				t.Fatalf("todo rechazo es el mismo error: %v", err)
			}
			if h.mailboxes.passwords[m.ID] != "" || h.appPasswords.deleted != 0 || len(h.events.subjects) != 0 {
				t.Fatalf("un rechazo no cambia nada: %v", h.events.subjects)
			}
		})
	}
}

func TestLaRecuperacionNoGastaElCodigoSiElTOTPFalla(t *testing.T) {
	h := newHarness()
	m := h.addMailbox(uuid.New(), "ana@acme.test", 0)
	codes := activar(t, h, "ana@acme.test")
	ctx := context.Background()

	if _, err := h.uc.RecoverPasswordByUsername(ctx, "ana@acme.test", "999999", codes[0], contrasenaNueva); !errors.Is(err, domain.ErrPasswordRecoveryRejected) {
		t.Fatal(err)
	}
	if got := len(h.mfa.rows[m.ID].RecoveryHashes); got != domain.RecoveryCodeCount {
		t.Fatalf("el codigo de recuperacion no se gasta si el TOTP falla: quedan %d", got)
	}
}

func TestLaRecuperacionExigeUnaContrasenaValidaAntesDeMirarElBuzon(t *testing.T) {
	h := newHarness()
	h.addMailbox(uuid.New(), "ana@acme.test", 0)
	codes := activar(t, h, "ana@acme.test")
	h.totp.codes["222222"] = 101

	for _, username := range []string{"ana@acme.test", "nadie@acme.test"} {
		if _, err := h.uc.RecoverPasswordByUsername(context.Background(), username, "222222", codes[0], "corta"); !errors.Is(err, domain.ErrPasswordTooShort) {
			t.Fatalf("%s: la politica responde igual exista o no el buzon: %v", username, err)
		}
	}
	if len(h.mfa.rows) != 1 || len(h.mfa.rows[h.mailboxes.items[0].ID].RecoveryHashes) != domain.RecoveryCodeCount {
		t.Fatal("una contrasena rechazada no gasta ningun codigo")
	}
}
