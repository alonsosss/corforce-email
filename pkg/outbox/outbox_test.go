package outbox

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/jackc/pgx/v5/pgconn"
	"go.uber.org/zap"
)

// Sin cerrojo el rele no toca la base: con pool nil, un vaciado entraria en panico.
func TestRunExclusiveSinCerrojoNoVacia(t *testing.T) {
	r := NewRelay(nil, nil, zap.NewNop(), Options{Interval: 5 * time.Millisecond, Retention: time.Hour})
	var intentos atomic.Int32
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	r.RunExclusive(ctx, func(context.Context) (func(), bool) {
		intentos.Add(1)
		return nil, false
	})
	if intentos.Load() < 2 {
		t.Fatalf("el rele debe reintentar el cerrojo en cada vuelta: %d intentos", intentos.Load())
	}
}

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
