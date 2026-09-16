package app

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
)

// Una contrasena de aplicacion que pierde un inicio de sesion se anuncia en su misma transaccion
// (mail.mailbox.credentials_changed con credential app_password): mail-security la retira de la
// cache de Dovecot y cierra las sesiones del buzon. Lo que no le quita nada no se anuncia, para no
// echar de Dovecot a quien sigue pudiendo entrar.
func TestCambiosDeUnaContrasenaDeAplicacionQueSeAnuncian(t *testing.T) {
	yes, no, name := true, false, "tableta"
	cases := []struct {
		desc  string
		start func(*domain.AppPassword)
		req   UpdateAppPasswordRequest
		want  bool
	}{
		{"renombrar", nil, UpdateAppPasswordRequest{Name: &name}, false},
		{"desactivar", nil, UpdateAppPasswordRequest{Active: &no}, true},
		{"reactivar", func(p *domain.AppPassword) { p.Active = false }, UpdateAppPasswordRequest{Active: &yes}, false},
		{"quitar imap", nil, UpdateAppPasswordRequest{IMAPAccess: &no}, true},
		{"quitar pop3", nil, UpdateAppPasswordRequest{POP3Access: &no}, true},
		{"quitar smtp", nil, UpdateAppPasswordRequest{SMTPAccess: &no}, true},
		{"quitar sieve", nil, UpdateAppPasswordRequest{SieveAccess: &no}, true},
		{"quitar dav, que la plataforma no sirve", nil, UpdateAppPasswordRequest{DAVAccess: &no}, false},
		{"devolver imap", func(p *domain.AppPassword) { p.IMAPAccess = false }, UpdateAppPasswordRequest{IMAPAccess: &yes}, false},
		{"repetir lo que ya tiene", nil, UpdateAppPasswordRequest{Active: &yes, IMAPAccess: &yes}, false},
		{"quitar imap a una inactiva", func(p *domain.AppPassword) { p.Active = false }, UpdateAppPasswordRequest{IMAPAccess: &no}, false},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			h := newHarness()
			tenant := uuid.New()
			m := h.addMailbox(tenant, "ana@acme.com", 0)
			p := h.addAppPassword(m, c.start)
			got, err := h.uc.UpdateAppPassword(context.Background(), tenant, m.ID, p.ID, c.req)
			if err != nil {
				t.Fatalf("UpdateAppPassword: %v", err)
			}
			stored, _ := h.appPasswords.Get(context.Background(), tenant, m.ID, p.ID)
			if !reflect.DeepEqual(got, stored) {
				t.Fatalf("respuesta %+v distinta de lo guardado %+v", got, stored)
			}
			var want []credentialEvent
			if c.want {
				want = []credentialEvent{{username: m.Username, credential: domain.CredentialAppPassword,
					changed: []domain.MailboxAttr{domain.AttrAppPassword}}}
			}
			if !reflect.DeepEqual(h.events.credentials, want) || len(h.events.subjects) != len(want) || len(h.events.outside) != 0 {
				t.Fatalf("avisos %+v (eventos %v, fuera de la transaccion %v); want %+v",
					h.events.credentials, h.events.subjects, h.events.outside, want)
			}
		})
	}
}

// Borrar una contrasena activa se anuncia; una inactiva ya no abria nada y su borrado no.
func TestBorrarUnaContrasenaDeAplicacion(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	tenant := uuid.New()
	m := h.addMailbox(tenant, "ana@acme.com", 0)
	activa := h.addAppPassword(m, nil)
	inactiva := h.addAppPassword(m, func(p *domain.AppPassword) { p.Active = false })

	if err := h.uc.DeleteAppPassword(ctx, tenant, m.ID, activa.ID); err != nil {
		t.Fatalf("borrar la activa: %v", err)
	}
	want := []credentialEvent{{username: m.Username, credential: domain.CredentialAppPassword,
		changed: []domain.MailboxAttr{domain.AttrAppPassword}}}
	if !reflect.DeepEqual(h.events.credentials, want) || len(h.events.outside) != 0 {
		t.Fatalf("avisos %+v (fuera de la transaccion %v); want %+v", h.events.credentials, h.events.outside, want)
	}
	if err := h.uc.DeleteAppPassword(ctx, tenant, m.ID, inactiva.ID); err != nil {
		t.Fatalf("borrar la inactiva: %v", err)
	}
	if err := h.uc.DeleteAppPassword(ctx, tenant, m.ID, activa.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("borrar otra vez: %v", err)
	}
	if err := h.uc.DeleteAppPassword(ctx, uuid.New(), m.ID, inactiva.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("borrar la de otra empresa: %v", err)
	}
	if len(h.appPasswords.items) != 0 || !reflect.DeepEqual(h.events.credentials, want) {
		t.Fatalf("quedan %d contrasenas; avisos %+v", len(h.appPasswords.items), h.events.credentials)
	}
}

// Una contrasena nueva no deja ninguna vieja valiendo en Dovecot: su alta no se anuncia.
func TestAltaDeUnaContrasenaDeAplicacionSinAviso(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	m := h.addMailbox(tenant, "ana@acme.com", 0)
	if _, _, err := h.uc.CreateAppPassword(context.Background(), tenant, m.ID, CreateAppPasswordRequest{Name: "movil"}); err != nil {
		t.Fatalf("alta: %v", err)
	}
	if len(h.events.subjects) != 0 {
		t.Fatalf("el alta anuncio %v", h.events.subjects)
	}
}

// Si el aviso no se puede encolar, la contrasena sigue como estaba: una revocacion confirmada sin
// aviso dejaria la credencial valiendo en la cache de Dovecot y sus sesiones abiertas.
func TestRevocarSinOutboxNoCambiaLaContrasena(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	tenant := uuid.New()
	m := h.addMailbox(tenant, "ana@acme.com", 0)
	p := h.addAppPassword(m, nil)
	caida := errors.New("outbox: encolar: permission denied for table event_outbox")
	h.events.fail = caida
	no := false

	if _, err := h.uc.UpdateAppPassword(ctx, tenant, m.ID, p.ID, UpdateAppPasswordRequest{Active: &no}); !errors.Is(err, caida) {
		t.Fatalf("desactivar sin outbox: %v", err)
	}
	if err := h.uc.DeleteAppPassword(ctx, tenant, m.ID, p.ID); !errors.Is(err, caida) {
		t.Fatalf("borrar sin outbox: %v", err)
	}
	stored, err := h.appPasswords.Get(ctx, tenant, m.ID, p.ID)
	if err != nil || !stored.Active || h.tx.rolledBack != 2 || len(h.events.credentials) != 0 {
		t.Fatalf("contrasena %+v (%v), rollbacks %d, avisos %+v", stored, err, h.tx.rolledBack, h.events.credentials)
	}
}

// Apagar un buzon revoca sus contrasenas de aplicacion y, como le quita sus inicios de sesion a todas
// sus credenciales, lo anuncia una vez como la principal (domain.MailboxLoginsRevoked), ademas de
// mail.mailbox.updated: sus sesiones se cierran en Dovecot aunque el buzon se reactive antes de que
// mail-security atienda el cambio. Un buzon sin protocolos de inicio no tenia sesion que cerrar, ni
// siquiera con sus contrasenas de aplicacion, que mail-auth acota por los flags del buzon.
func TestApagarUnBuzonAnunciaSusContrasenasDeAplicacion(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	tenant := uuid.New()
	ana := withAllLogins(h.addMailbox(tenant, "ana@acme.com", 0))
	bea := withAllLogins(h.addMailbox(tenant, "bea@acme.com", 0))
	h.addAppPassword(ana, nil)
	h.addAppPassword(ana, nil)
	h.addAppPassword(ana, func(p *domain.AppPassword) { p.Active = false })
	deBea := h.addAppPassword(bea, nil)
	off := domain.ActiveOff

	if _, err := h.uc.UpdateMailbox(ctx, tenant, ana.ID, UpdateMailboxRequest{Active: &off}); err != nil {
		t.Fatalf("apagar a ana: %v", err)
	}
	wantSubjects := []string{"mail.mailbox.credentials_changed", "mail.mailbox.updated"}
	wantCredentials := []credentialEvent{{username: ana.Username, credential: domain.CredentialPassword,
		changed: []domain.MailboxAttr{domain.AttrActive}}}
	if !reflect.DeepEqual(h.events.subjects, wantSubjects) || !reflect.DeepEqual(h.events.credentials, wantCredentials) || len(h.events.outside) != 0 {
		t.Fatalf("eventos %v, avisos %+v, fuera de la transaccion %v", h.events.subjects, h.events.credentials, h.events.outside)
	}
	for _, p := range h.appPasswords.items {
		if p.MailboxID == ana.ID && p.Active {
			t.Fatalf("contrasena de ana sigue activa: %+v", p)
		}
	}
	if stored, _ := h.appPasswords.Get(ctx, tenant, bea.ID, deBea.ID); !stored.Active {
		t.Fatal("apagar a ana no toca las contrasenas de bea")
	}

	h.events.subjects, h.events.credentials = nil, nil
	on := domain.ActiveOn
	if _, err := h.uc.UpdateMailbox(ctx, tenant, ana.ID, UpdateMailboxRequest{Active: &on}); err != nil {
		t.Fatalf("reactivar a ana: %v", err)
	}
	if _, err := h.uc.UpdateMailbox(ctx, tenant, ana.ID, UpdateMailboxRequest{Active: &off}); err != nil {
		t.Fatalf("apagarla sin contrasenas activas: %v", err)
	}
	want := []string{"mail.mailbox.updated", "mail.mailbox.credentials_changed", "mail.mailbox.updated"}
	if !reflect.DeepEqual(h.events.subjects, want) || !reflect.DeepEqual(h.events.credentials, wantCredentials) {
		t.Fatalf("eventos %v, avisos %+v; want %v", h.events.subjects, h.events.credentials, want)
	}

	h.events.subjects, h.events.credentials = nil, nil
	sinProtocolos := h.addMailbox(tenant, "recursos@acme.com", 0)
	h.addAppPassword(sinProtocolos, nil)
	if _, err := h.uc.UpdateMailbox(ctx, tenant, sinProtocolos.ID, UpdateMailboxRequest{Active: &off}); err != nil {
		t.Fatalf("apagar un buzon sin protocolos: %v", err)
	}
	if want := []string{"mail.mailbox.updated"}; !reflect.DeepEqual(h.events.subjects, want) {
		t.Fatalf("eventos %v; want %v", h.events.subjects, want)
	}
}
