package domain

import "testing"

func TestAppPasswordLoginsRevoked(t *testing.T) {
	full := AppPassword{Active: true, IMAPAccess: true, POP3Access: true, SMTPAccess: true, SieveAccess: true, DAVAccess: true}
	with := func(change func(*AppPassword)) *AppPassword {
		p := full
		change(&p)
		return &p
	}
	cases := []struct {
		name   string
		before AppPassword
		after  *AppPassword
		want   bool
	}{
		{"sin cambios", full, with(func(*AppPassword) {}), false},
		{"renombrada", full, with(func(p *AppPassword) { p.Name = "tableta" }), false},
		{"desactivada", full, with(func(p *AppPassword) { p.Active = false }), true},
		{"borrada activa", full, nil, true},
		{"sin imap", full, with(func(p *AppPassword) { p.IMAPAccess = false }), true},
		{"sin pop3", full, with(func(p *AppPassword) { p.POP3Access = false }), true},
		{"sin smtp", full, with(func(p *AppPassword) { p.SMTPAccess = false }), true},
		{"sin sieve", full, with(func(p *AppPassword) { p.SieveAccess = false }), true},
		{"sin dav: mail-dav no guarda sesion ni cache que cerrar", full, with(func(p *AppPassword) { p.DAVAccess = false }), false},
		{"reactivada", *with(func(p *AppPassword) { p.Active = false }), &full, false},
		{"con mas protocolos", *with(func(p *AppPassword) { p.IMAPAccess = false }), &full, false},
		{"borrada inactiva", *with(func(p *AppPassword) { p.Active = false }), nil, false},
		{"borrada sin protocolos de inicio", AppPassword{Active: true, DAVAccess: true}, nil, false},
		{"desactivada y con mas protocolos a la vez", *with(func(p *AppPassword) { p.IMAPAccess = false }),
			with(func(p *AppPassword) { p.Active = false }), true},
		{"cambia un protocolo por otro", *with(func(p *AppPassword) { p.POP3Access = false }),
			with(func(p *AppPassword) { p.IMAPAccess = false }), true},
	}
	for _, c := range cases {
		if got := AppPasswordLoginsRevoked(c.before, c.after); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestMailboxLoginsRevoked(t *testing.T) {
	full := Mailbox{Username: "ana@acme.test", Active: ActiveOn, IMAPAccess: true, POP3Access: true, SMTPAccess: true, SieveAccess: true, DAVAccess: true}
	with := func(change func(*Mailbox)) Mailbox {
		m := full
		change(&m)
		return m
	}
	cases := []struct {
		name          string
		before, after Mailbox
		want          bool
	}{
		{"sin cambios", full, full, false},
		{"cuota, nombre, TLS y reenvio de contrasena", full, with(func(m *Mailbox) {
			m.QuotaBytes, m.DisplayName, m.TLSEnforceIn, m.TLSEnforceOut, m.ForcePwUpdate = 1, "Ana", true, true, true
		}), false},
		{"sin imap", full, with(func(m *Mailbox) { m.IMAPAccess = false }), true},
		{"sin pop3", full, with(func(m *Mailbox) { m.POP3Access = false }), true},
		{"sin smtp", full, with(func(m *Mailbox) { m.SMTPAccess = false }), true},
		{"sin sieve", full, with(func(m *Mailbox) { m.SieveAccess = false }), true},
		{"sin dav: mail-dav no guarda sesion ni cache que cerrar", full, with(func(m *Mailbox) { m.DAVAccess = false }), false},
		{"apagado", full, with(func(m *Mailbox) { m.Active = ActiveOff }), true},
		{"solo recepcion", full, with(func(m *Mailbox) { m.Active = ActiveReceiveOnly }), true},
		{"de solo recepcion a apagado", with(func(m *Mailbox) { m.Active = ActiveReceiveOnly }), with(func(m *Mailbox) { m.Active = ActiveOff }), false},
		{"reactivado", with(func(m *Mailbox) { m.Active = ActiveOff }), full, false},
		{"devolver imap", with(func(m *Mailbox) { m.IMAPAccess = false }), full, false},
		{"quitar imap a un buzon apagado", with(func(m *Mailbox) { m.Active = ActiveOff }),
			with(func(m *Mailbox) { m.Active, m.IMAPAccess = ActiveOff, false }), false},
		{"reactivado sin imap", with(func(m *Mailbox) { m.Active = ActiveOff }), with(func(m *Mailbox) { m.IMAPAccess = false }), false},
		{"cambia un protocolo por otro", with(func(m *Mailbox) { m.POP3Access = false }), with(func(m *Mailbox) { m.IMAPAccess = false }), true},
	}
	for _, c := range cases {
		if got := MailboxLoginsRevoked(c.before, c.after); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCredentialValid(t *testing.T) {
	for _, c := range []Credential{CredentialPassword, CredentialAppPassword} {
		if !c.Valid() {
			t.Errorf("%q deberia ser valida", c)
		}
	}
	for _, c := range []Credential{"", "PASSWORD", "dav"} {
		if c.Valid() {
			t.Errorf("%q no deberia ser valida", c)
		}
	}
}
