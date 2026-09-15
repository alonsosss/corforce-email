package domain

import (
	"errors"
	"fmt"
	"testing"
)

// Borrado y credencial cambiada cierran siempre las sesiones; una actualizacion las cierra solo
// si el buzon ya no puede iniciar sesion segun el directorio (apagado, solo recepcion o ya
// borrado). Un buzon activo al que se le cambia la cuota o el nombre solo pierde su cache.
func TestSessionActionForSigueAlEstadoReal(t *testing.T) {
	activo := &Mailbox{Username: "ana@acme.test", Active: 1}
	apagado := &Mailbox{Username: "ana@acme.test", Active: 0}
	soloRecibe := &Mailbox{Username: "ana@acme.test", Active: 2}
	cases := []struct {
		change  MailboxChange
		current *Mailbox
		want    SessionAction
	}{
		{MailboxUpdated, activo, SessionFlush},
		{MailboxUpdated, apagado, SessionKick},
		{MailboxUpdated, soloRecibe, SessionKick},
		{MailboxUpdated, nil, SessionKick},
		{MailboxDeleted, nil, SessionKick},
		{MailboxDeleted, activo, SessionKick},
		{MailboxCredentialsChanged, activo, SessionKick},
		{MailboxCredentialsChanged, nil, SessionKick},
	}
	for _, c := range cases {
		if got := SessionActionFor(c.change, c.current); got != c.want {
			t.Errorf("%s con %+v: %s, se esperaba %s", c.change, c.current, got, c.want)
		}
	}
}

func TestMailboxChangeOf(t *testing.T) {
	for subject, want := range map[string]MailboxChange{
		"mail.mailbox.updated":             MailboxUpdated,
		"mail.mailbox.deleted":             MailboxDeleted,
		"mail.mailbox.credentials_changed": MailboxCredentialsChanged,
	} {
		if got, ok := MailboxChangeOf(subject); !ok || got != want {
			t.Errorf("%s: %q %v", subject, got, ok)
		}
	}
	for _, subject := range []string{"mail.mailbox.created", "mail.domain.updated", ""} {
		if _, ok := MailboxChangeOf(subject); ok {
			t.Errorf("%s no revoca nada", subject)
		}
	}
}

// El nombre va a la mascara de doveadm kick, que admite comodines: solo pasa un buzon con la
// forma que admite mail-directory, en minusculas como lo guarda Dovecot.
func TestNormalizeSessionUser(t *testing.T) {
	ok := map[string]string{
		"ana@acme.test":           "ana@acme.test",
		" Ana.Perez@Acme.Test ":   "ana.perez@acme.test",
		"ana+ventas@sub.acme.com": "ana+ventas@sub.acme.com",
		"a_b-c@xn--acm-9la.test":  "a_b-c@xn--acm-9la.test",
	}
	for in, want := range ok {
		got, err := NormalizeSessionUser(in)
		if err != nil || got != want {
			t.Errorf("%q: %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "ana", "@acme.test", "ana@", "*@acme.test", "an?@acme.test", "ana@*.test",
		"ana@acme", "a b@acme.test", "ana@acme.test\nbea@acme.test", "ana@@acme.test", "ana@acme.test*", fmt.Sprintf("%065d@acme.test", 0)} {
		if got, err := NormalizeSessionUser(in); err == nil {
			t.Errorf("%q se acepto como %q", in, got)
		}
	}
}

func TestRevocationFailureOf(t *testing.T) {
	cases := map[error]SessionRevocationFailure{
		fmt.Errorf("x: %w", ErrEngineUnreachable): RevocationUnreachable,
		fmt.Errorf("x: %w", ErrEngineRejected):    RevocationRejected,
		fmt.Errorf("x: %w", ErrEngineCommand):     RevocationCommand,
		errors.New("otro"):                        RevocationCommand,
	}
	for err, want := range cases {
		if got := RevocationFailureOf(err); got != want {
			t.Errorf("%v: %s, se esperaba %s", err, got, want)
		}
	}
}
