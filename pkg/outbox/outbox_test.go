package outbox

import (
	"context"
	"errors"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/jackc/pgx/v5/pgconn"
)

type execGrabador struct {
	sql  string
	args []interface{}
	err  error
}

func (e *execGrabador) Exec(_ context.Context, sql string, args ...interface{}) (pgconn.CommandTag, error) {
	e.sql, e.args = sql, args
	return pgconn.CommandTag{}, e.err
}

func TestEnqueueRellenaIdYTiempoYGuardaElTenant(t *testing.T) {
	g := &execGrabador{}
	evt := events.Event{Type: "mail.domain.created", Source: "mail-directory", TenantID: "t-1", Data: map[string]any{"domain": "a.test"}}
	if err := Enqueue(context.Background(), g, "mail.domain.created", evt); err != nil {
		t.Fatal(err)
	}
	if len(g.args) != 4 || g.args[0] == "" || g.args[1] != "mail.domain.created" {
		t.Fatalf("argumentos inesperados: %v", g.args)
	}
	if tid, ok := g.args[2].(*string); !ok || *tid != "t-1" {
		t.Fatalf("tenant no guardado: %v", g.args[2])
	}
}

func TestEnqueueRechazaSubjectVacioYPropagaErrores(t *testing.T) {
	if err := Enqueue(context.Background(), &execGrabador{}, "", events.Event{}); err == nil {
		t.Fatal("subject vacio debe fallar")
	}
	g := &execGrabador{err: errors.New("caida")}
	if err := Enqueue(context.Background(), g, "x.y.z", events.Event{}); err == nil {
		t.Fatal("el error de la base debe propagarse")
	}
}
