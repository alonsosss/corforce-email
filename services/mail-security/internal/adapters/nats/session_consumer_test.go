package nats

import (
	"context"
	"fmt"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"go.uber.org/zap"
)

func consumidorDeSesiones(dir *apptest.Directory, engine *apptest.EngineSessions) *SessionConsumer {
	revoker := app.NewSessionRevoker(app.SessionRevokerDeps{Directory: dir, Engine: engine})
	return NewSessionConsumer(nil, revoker, func(ctx context.Context) context.Context { return ctx }, zap.NewNop())
}

func entregar(c *SessionConsumer, subject string, data map[string]any) bool {
	acked := false
	c.handle(events.Event{Type: subject, Data: data}, func() { acked = true })
	return acked
}

// Cada evento de buzon llega a Dovecot con el nombre en minusculas y la accion del estado real;
// uno que no revoca nada o sin un buzon valido se confirma sin llamar a Dovecot.
func TestLosEventosDeBuzonRevocanEnDovecot(t *testing.T) {
	dir := apptest.NewDirectory()
	dir.Mailboxes["ana@acme.test"] = domain.Mailbox{Username: "ana@acme.test", Active: 1}
	dir.Mailboxes["bea@acme.test"] = domain.Mailbox{Username: "bea@acme.test", Active: 0}
	engine := &apptest.EngineSessions{}
	c := consumidorDeSesiones(dir, engine)

	pasos := []struct {
		subject  string
		username any
	}{
		{"mail.mailbox.updated", "Ana@Acme.Test"},
		{"mail.mailbox.updated", "bea@acme.test"},
		{"mail.mailbox.deleted", "carla@acme.test"},
		{"mail.mailbox.credentials_changed", "ana@acme.test"},
		{"mail.mailbox.created", "dora@acme.test"},
		{"mail.mailbox.updated", "*@acme.test"},
		{"mail.mailbox.updated", ""},
		{"mail.mailbox.updated", 42},
	}
	for _, p := range pasos {
		if !entregar(c, p.subject, map[string]any{"username": p.username}) {
			t.Fatalf("%s %v sin confirmar", p.subject, p.username)
		}
	}
	want := []apptest.SessionCall{
		{Username: "ana@acme.test", Kick: false},
		{Username: "bea@acme.test", Kick: true},
		{Username: "carla@acme.test", Kick: true},
		{Username: "ana@acme.test", Kick: true},
	}
	if fmt.Sprint(engine.Calls) != fmt.Sprint(want) {
		t.Fatalf("llamadas a Dovecot: %v, se esperaba %v", engine.Calls, want)
	}
}

// Con Dovecot sin responder el evento queda sin confirmar para que JetStream lo reentregue, y la
// reentrega lo aplica cuando vuelve.
func TestUnFalloDeDovecotSeReentrega(t *testing.T) {
	engine := &apptest.EngineSessions{Err: fmt.Errorf("sin red: %w", domain.ErrEngineUnreachable)}
	c := consumidorDeSesiones(apptest.NewDirectory(), engine)
	data := map[string]any{"username": "ana@acme.test"}
	if entregar(c, "mail.mailbox.deleted", data) {
		t.Fatal("un fallo no se confirma")
	}
	engine.Err = nil
	if !entregar(c, "mail.mailbox.deleted", data) {
		t.Fatal("la reentrega se confirma")
	}
	if len(engine.Calls) != 2 || !engine.Calls[1].Kick {
		t.Fatalf("llamadas %v", engine.Calls)
	}
}
