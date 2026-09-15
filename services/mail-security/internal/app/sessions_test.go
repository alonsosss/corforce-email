package app

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/ports"
)

// directorioCaido falla al leer un buzon, como una base de la celda que no responde.
type directorioCaido struct{ *apptest.Directory }

func (directorioCaido) MailboxByUsername(context.Context, string) (*domain.Mailbox, error) {
	return nil, errors.New("sin conexion")
}

func revocador(dir ports.DirectoryReader, engine *apptest.EngineSessions, metrics *apptest.SessionMetrics) *SessionRevoker {
	return NewSessionRevoker(SessionRevokerDeps{Directory: dir, Engine: engine, Metrics: metrics})
}

// Cada cambio llega a Dovecot con la accion que pide el estado real del buzon, y cuenta.
func TestRevokeDecideConElDirectorio(t *testing.T) {
	ctx := context.Background()
	dir := apptest.NewDirectory()
	dir.Mailboxes["ana@acme.test"] = domain.Mailbox{Username: "ana@acme.test", Active: 1}
	dir.Mailboxes["bea@acme.test"] = domain.Mailbox{Username: "bea@acme.test", Active: 0}
	engine, metrics := &apptest.EngineSessions{}, apptest.NewSessionMetrics()
	uc := revocador(dir, engine, metrics)

	pasos := []struct {
		change   domain.MailboxChange
		username string
	}{
		{domain.MailboxUpdated, "ana@acme.test"},
		{domain.MailboxUpdated, "bea@acme.test"},
		{domain.MailboxUpdated, "nadie@acme.test"},
		{domain.MailboxDeleted, "ana@acme.test"},
		{domain.MailboxCredentialsChanged, "ana@acme.test"},
	}
	for _, p := range pasos {
		if err := uc.Revoke(ctx, p.change, p.username); err != nil {
			t.Fatalf("%s %s: %v", p.change, p.username, err)
		}
	}
	want := []apptest.SessionCall{
		{Username: "ana@acme.test", Kick: false},
		{Username: "bea@acme.test", Kick: true},
		{Username: "nadie@acme.test", Kick: true},
		{Username: "ana@acme.test", Kick: true},
		{Username: "ana@acme.test", Kick: true},
	}
	if fmt.Sprint(engine.Calls) != fmt.Sprint(want) {
		t.Fatalf("llamadas a Dovecot: %v, se esperaba %v", engine.Calls, want)
	}
	if metrics.Revoked[domain.SessionFlush] != 1 || metrics.Revoked[domain.SessionKick] != 4 || len(metrics.Failed) != 0 {
		t.Fatalf("metricas: %v / %v", metrics.Revoked, metrics.Failed)
	}
}

// Sin el estado del buzon no se decide nada ni se llama a Dovecot: el error vuelve para que el
// evento se reentregue. Un fallo de Dovecot cuenta con su motivo.
func TestRevokeCuentaLosFallosPorMotivo(t *testing.T) {
	ctx := context.Background()
	engine, metrics := &apptest.EngineSessions{}, apptest.NewSessionMetrics()
	if err := revocador(directorioCaido{apptest.NewDirectory()}, engine, metrics).Revoke(ctx, domain.MailboxUpdated, "ana@acme.test"); err == nil {
		t.Fatal("sin directorio no hay revocacion")
	}
	if len(engine.Calls) != 0 || metrics.Failed[domain.RevocationDirectory] != 1 {
		t.Fatalf("llamadas %v, fallos %v", engine.Calls, metrics.Failed)
	}

	for err, reason := range map[error]domain.SessionRevocationFailure{
		fmt.Errorf("x: %w", domain.ErrEngineUnreachable): domain.RevocationUnreachable,
		fmt.Errorf("x: %w", domain.ErrEngineRejected):    domain.RevocationRejected,
		fmt.Errorf("x: %w", domain.ErrEngineCommand):     domain.RevocationCommand,
	} {
		engine.Err = err
		if got := revocador(apptest.NewDirectory(), engine, metrics).Revoke(ctx, domain.MailboxDeleted, "ana@acme.test"); !errors.Is(got, err) {
			t.Fatalf("%v: devolvio %v", err, got)
		}
		if metrics.Failed[reason] != 1 {
			t.Fatalf("%v: fallos %v", err, metrics.Failed)
		}
	}
	if len(metrics.Revoked) != 0 {
		t.Fatalf("un fallo no cuenta como revocacion: %v", metrics.Revoked)
	}
}
