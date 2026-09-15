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
		{"sin dav: la plataforma no sirve DAV", full, with(func(p *AppPassword) { p.DAVAccess = false }), false},
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
