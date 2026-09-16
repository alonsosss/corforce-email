package domain

import (
	"reflect"
	"testing"

	"github.com/google/uuid"
)

// MailboxChanges nombra exactamente lo que cambio, siempre en el mismo orden: es lo que el webmail
// lee para decidir si cierra las sesiones del buzon, asi que un atributo de menos deja abierta una
// sesion que ya no vale.
func TestMailboxChanges(t *testing.T) {
	relay, otro := uuid.New(), uuid.New()
	full := Mailbox{Username: "ana@acme.test", Active: ActiveOn, IMAPAccess: true, POP3Access: true,
		SMTPAccess: true, SieveAccess: true, QuotaBytes: 1 << 20, DisplayName: "Ana"}
	with := func(change func(*Mailbox)) Mailbox {
		m := full
		change(&m)
		return m
	}
	cases := []struct {
		name  string
		after Mailbox
		want  []MailboxAttr
	}{
		{"sin cambios", full, nil},
		{"nombre visible", with(func(m *Mailbox) { m.DisplayName = "Ana P." }), []MailboxAttr{AttrDisplayName}},
		{"cuota", with(func(m *Mailbox) { m.QuotaBytes = 2 << 20 }), []MailboxAttr{AttrQuotaBytes}},
		{"apagado", with(func(m *Mailbox) { m.Active = ActiveOff }), []MailboxAttr{AttrActive}},
		{"recurso", with(func(m *Mailbox) { m.Kind = "location" }), []MailboxAttr{AttrKind}},
		{"TLS", with(func(m *Mailbox) { m.TLSEnforceIn, m.TLSEnforceOut = true, true }),
			[]MailboxAttr{AttrTLSEnforceIn, AttrTLSEnforceOut}},
		{"relayhost nuevo", with(func(m *Mailbox) { m.RelayhostID = &relay }), []MailboxAttr{AttrRelayhostID}},
		{"sin imap", with(func(m *Mailbox) { m.IMAPAccess = false }), []MailboxAttr{AttrIMAPAccess}},
		{"sin pop3", with(func(m *Mailbox) { m.POP3Access = false }), []MailboxAttr{AttrPOP3Access}},
		{"sin smtp", with(func(m *Mailbox) { m.SMTPAccess = false }), []MailboxAttr{AttrSMTPAccess}},
		{"sin sieve", with(func(m *Mailbox) { m.SieveAccess = false }), []MailboxAttr{AttrSieveAccess}},
		{"reenvio de contrasena", with(func(m *Mailbox) { m.ForcePwUpdate = true }), []MailboxAttr{AttrForcePwUpdate}},
		{"cuota y nombre a la vez", with(func(m *Mailbox) { m.DisplayName, m.QuotaBytes = "Otra", 3<<20 }),
			[]MailboxAttr{AttrDisplayName, AttrQuotaBytes}},
		{"cambia un protocolo por otro", with(func(m *Mailbox) { m.IMAPAccess, m.POP3Access = false, false }),
			[]MailboxAttr{AttrIMAPAccess, AttrPOP3Access}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := MailboxChanges(full, c.after); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}

	// El relayhost se compara por valor, no por puntero: dos punteros distintos al mismo id no
	// son un cambio, y el mismo id en otro puntero tampoco.
	mismo := relay
	if got := MailboxChanges(with(func(m *Mailbox) { m.RelayhostID = &relay }), with(func(m *Mailbox) { m.RelayhostID = &mismo })); got != nil {
		t.Fatalf("el mismo relayhost en otro puntero no es un cambio: %v", got)
	}
	if got := MailboxChanges(with(func(m *Mailbox) { m.RelayhostID = &relay }), with(func(m *Mailbox) { m.RelayhostID = &otro })); !reflect.DeepEqual(got, []MailboxAttr{AttrRelayhostID}) {
		t.Fatalf("otro relayhost si lo es: %v", got)
	}
	if got := MailboxChanges(with(func(m *Mailbox) { m.RelayhostID = &relay }), full); !reflect.DeepEqual(got, []MailboxAttr{AttrRelayhostID}) {
		t.Fatalf("quitar el relayhost es un cambio: %v", got)
	}
}

func TestMailboxAttrValid(t *testing.T) {
	for _, a := range []MailboxAttr{AttrDisplayName, AttrQuotaBytes, AttrActive, AttrKind, AttrTLSEnforceIn,
		AttrTLSEnforceOut, AttrRelayhostID, AttrIMAPAccess, AttrPOP3Access, AttrSMTPAccess, AttrSieveAccess,
		AttrForcePwUpdate, AttrPassword, AttrAppPassword} {
		if !a.Valid() {
			t.Errorf("%q deberia ser valido", a)
		}
	}
	for _, a := range []MailboxAttr{"", "ACTIVE", "dav_access", "username"} {
		if a.Valid() {
			t.Errorf("%q no deberia ser valido", a)
		}
	}
}
