package app

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
)

// withAllLogins deja el buzon activo con los cuatro protocolos de inicio de sesion.
func withAllLogins(m *domain.Mailbox) *domain.Mailbox {
	m.Active, m.IMAPAccess, m.POP3Access, m.SMTPAccess, m.SieveAccess = domain.ActiveOn, true, true, true, true
	return m
}

// Un cambio del buzon que le quita un inicio de sesion (domain.MailboxLoginsRevoked) sale en su misma
// transaccion como mail.mailbox.credentials_changed con credential password, antes de
// mail.mailbox.updated: mail-security cierra en Dovecot la sesion ya abierta con el protocolo
// retirado, que el cambio del buzon activo solo vaciaria de la cache. Lo que no retira nada sale solo
// como mail.mailbox.updated.
//
// Los dos avisos llevan ademas los atributos que cambiaron: con ellos el webmail revoca solo cuando
// alguno invalida su sesion, en vez de revocar con cualquier cambio.
func TestCambiosDelBuzonQueLeQuitanUnInicioDeSesion(t *testing.T) {
	yes, no := true, false
	name, quota := "Ana P.", int64(1<<20)
	off, receiveOnly, on := domain.ActiveOff, domain.ActiveReceiveOnly, domain.ActiveOn
	cases := []struct {
		desc    string
		start   func(*domain.Mailbox)
		req     UpdateMailboxRequest
		want    bool
		changed []domain.MailboxAttr
	}{
		{"nombre visible", nil, UpdateMailboxRequest{DisplayName: &name}, false, []domain.MailboxAttr{domain.AttrDisplayName}},
		{"cuota", nil, UpdateMailboxRequest{QuotaBytes: &quota}, false, []domain.MailboxAttr{domain.AttrQuotaBytes}},
		{"TLS obligatorio", nil, UpdateMailboxRequest{TLSEnforceIn: &yes, TLSEnforceOut: &yes}, false,
			[]domain.MailboxAttr{domain.AttrTLSEnforceIn, domain.AttrTLSEnforceOut}},
		{"quitar imap", nil, UpdateMailboxRequest{IMAPAccess: &no}, true, []domain.MailboxAttr{domain.AttrIMAPAccess}},
		{"quitar pop3", nil, UpdateMailboxRequest{POP3Access: &no}, true, []domain.MailboxAttr{domain.AttrPOP3Access}},
		{"quitar smtp", nil, UpdateMailboxRequest{SMTPAccess: &no}, true, []domain.MailboxAttr{domain.AttrSMTPAccess}},
		{"quitar sieve", nil, UpdateMailboxRequest{SieveAccess: &no}, true, []domain.MailboxAttr{domain.AttrSieveAccess}},
		{"solo recepcion", nil, UpdateMailboxRequest{Active: &receiveOnly}, true, []domain.MailboxAttr{domain.AttrActive}},
		{"apagar", nil, UpdateMailboxRequest{Active: &off}, true, []domain.MailboxAttr{domain.AttrActive}},
		{"repetir lo que ya tiene", nil, UpdateMailboxRequest{Active: &on, IMAPAccess: &yes}, false, nil},
		{"devolver imap", func(m *domain.Mailbox) { m.IMAPAccess = false }, UpdateMailboxRequest{IMAPAccess: &yes}, false,
			[]domain.MailboxAttr{domain.AttrIMAPAccess}},
		{"reactivar", func(m *domain.Mailbox) { m.Active = domain.ActiveOff }, UpdateMailboxRequest{Active: &on}, false,
			[]domain.MailboxAttr{domain.AttrActive}},
		{"quitar imap a un buzon apagado", func(m *domain.Mailbox) { m.Active = domain.ActiveOff }, UpdateMailboxRequest{IMAPAccess: &no}, false,
			[]domain.MailboxAttr{domain.AttrIMAPAccess}},
		{"cambiar pop3 por imap", func(m *domain.Mailbox) { m.IMAPAccess = false },
			UpdateMailboxRequest{IMAPAccess: &yes, POP3Access: &no}, true,
			[]domain.MailboxAttr{domain.AttrIMAPAccess, domain.AttrPOP3Access}},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			h := newHarness()
			tenant := uuid.New()
			h.addDomain(tenant, "acme.com", domain.DomainLimits{})
			m := withAllLogins(h.addMailbox(tenant, "ana@acme.com", 0))
			if c.start != nil {
				c.start(m)
			}
			if _, err := h.uc.UpdateMailbox(context.Background(), tenant, m.ID, c.req); err != nil {
				t.Fatalf("UpdateMailbox: %v", err)
			}
			wantSubjects := []string{"mail.mailbox.updated"}
			var wantCredentials []credentialEvent
			if c.want {
				wantSubjects = []string{"mail.mailbox.credentials_changed", "mail.mailbox.updated"}
				wantCredentials = []credentialEvent{{username: m.Username, credential: domain.CredentialPassword, changed: c.changed}}
			}
			if !reflect.DeepEqual(h.events.subjects, wantSubjects) || !reflect.DeepEqual(h.events.credentials, wantCredentials) ||
				len(h.events.outside) != 0 {
				t.Fatalf("eventos %v, avisos %+v, fuera de la transaccion %v; want %v %+v",
					h.events.subjects, h.events.credentials, h.events.outside, wantSubjects, wantCredentials)
			}
			if !reflect.DeepEqual(h.events.changed, [][]domain.MailboxAttr{c.changed}) {
				t.Fatalf("changed de mail.mailbox.updated = %v; want %v", h.events.changed, c.changed)
			}
		})
	}
}

// Si el aviso no se puede encolar, el buzon conserva su protocolo: un flag retirado sin aviso dejaria
// abierta en Dovecot la sesion que ya lo usaba.
func TestQuitarUnProtocoloSinOutboxNoCambiaElBuzon(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	tenant := uuid.New()
	m := withAllLogins(h.addMailbox(tenant, "ana@acme.com", 0))
	caida := errors.New("outbox: encolar: permission denied for table event_outbox")
	h.events.fail = caida
	no := false

	if _, err := h.uc.UpdateMailbox(ctx, tenant, m.ID, UpdateMailboxRequest{IMAPAccess: &no}); !errors.Is(err, caida) {
		t.Fatalf("quitar imap sin outbox: %v", err)
	}
	stored, err := h.uc.GetMailbox(ctx, tenant, m.ID)
	if err != nil || !stored.IMAPAccess || h.tx.rolledBack != 1 || len(h.events.credentials) != 0 {
		t.Fatalf("buzon %+v (%v), rollbacks %d, avisos %+v", stored, err, h.tx.rolledBack, h.events.credentials)
	}
}

// El cambio de la contrasena principal se anuncia como tal (AttrPassword), no como un cambio del
// buzon: el webmail cierra sus sesiones con el, porque la credencial con la que se abrieron dejo de
// valer, aunque el buzon no haya cambiado en nada.
func TestCambiarLaContrasenaSeAnunciaComoLaCredencial(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	m := withAllLogins(h.addMailbox(tenant, "ana@acme.com", 0))

	if err := h.uc.SetMailboxPassword(context.Background(), tenant, m.ID, "contrasena-de-prueba-1"); err != nil {
		t.Fatalf("SetMailboxPassword: %v", err)
	}
	want := []credentialEvent{{username: m.Username, credential: domain.CredentialPassword,
		changed: []domain.MailboxAttr{domain.AttrPassword}}}
	if !reflect.DeepEqual(h.events.subjects, []string{"mail.mailbox.credentials_changed"}) ||
		!reflect.DeepEqual(h.events.credentials, want) || len(h.events.outside) != 0 {
		t.Fatalf("eventos %v, avisos %+v, fuera de la transaccion %v", h.events.subjects, h.events.credentials, h.events.outside)
	}
}
