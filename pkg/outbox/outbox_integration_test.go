//go:build integration

package outbox

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

type publicadorFalso struct {
	mu      sync.Mutex
	vistos  []string
	fallaEn map[string]bool
}

func (p *publicadorFalso) PublishPersistent(subject string, evt events.Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fallaEn[evt.ID] {
		return errors.New("jetstream caido")
	}
	p.vistos = append(p.vistos, subject+"|"+evt.ID)
	return nil
}

// OUTBOX_TEST_DSN apunta a una base con migrations/*/platform/00_outbox.sql aplicada.
func TestReleVaciaLaOutboxYReintentaLoQueFalla(t *testing.T) {
	dsn := os.Getenv("OUTBOX_TEST_DSN")
	if dsn == "" {
		t.Skip("OUTBOX_TEST_DSN no definido")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, "DELETE FROM platform.event_outbox"); err != nil {
		t.Fatal(err)
	}

	tx, _ := pool.Begin(ctx)
	e1 := events.Event{ID: "11111111-1111-4111-8111-111111111111", Type: "a.b.c", TenantID: "22222222-2222-4222-8222-222222222222"}
	e2 := events.Event{ID: "33333333-3333-4333-8333-333333333333", Type: "a.b.d"}
	if err := Enqueue(ctx, tx, "a.b.c", e1); err != nil {
		t.Fatal(err)
	}
	if err := Enqueue(ctx, tx, "a.b.d", e2); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	pub := &publicadorFalso{fallaEn: map[string]bool{e2.ID: true}}
	relay := NewRelay(pool, pub, zap.NewNop(), Options{Batch: 10, MaxAttempts: 3})
	n, err := relay.Drain(ctx)
	if err != nil || n != 2 {
		t.Fatalf("primer vaciado: n=%d err=%v", n, err)
	}
	if len(pub.vistos) != 1 || pub.vistos[0] != "a.b.c|"+e1.ID {
		t.Fatalf("publicado: %v", pub.vistos)
	}
	var pendientes int
	_ = pool.QueryRow(ctx, "SELECT count(*) FROM platform.event_outbox WHERE published_at IS NULL").Scan(&pendientes)
	if pendientes != 1 {
		t.Fatalf("debe quedar 1 pendiente, hay %d", pendientes)
	}

	pub.fallaEn = nil
	if _, err := relay.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	_ = pool.QueryRow(ctx, "SELECT count(*) FROM platform.event_outbox WHERE published_at IS NULL").Scan(&pendientes)
	if pendientes != 0 {
		t.Fatalf("tras reintentar no debe quedar nada, hay %d", pendientes)
	}
	if _, err := pool.Exec(ctx, "UPDATE platform.event_outbox SET published_at = now() - interval '2 days'"); err != nil {
		t.Fatal(err)
	}
	borradas, err := Purge(ctx, pool, 24*time.Hour)
	if err != nil || borradas != 2 {
		t.Fatalf("poda: %d %v", borradas, err)
	}
}

// Con RunExclusive solo vacia quien tiene el cerrojo, y la poda por retencion corre en la
// misma vuelta.
func TestRunExclusiveSoloVaciaConElCerrojo(t *testing.T) {
	dsn := os.Getenv("OUTBOX_TEST_DSN")
	if dsn == "" {
		t.Skip("OUTBOX_TEST_DSN no definido")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, "DELETE FROM platform.event_outbox"); err != nil {
		t.Fatal(err)
	}
	viejo := events.Event{ID: "44444444-4444-4444-8444-444444444444", Type: "a.b.viejo"}
	nuevo := events.Event{ID: "55555555-5555-4555-8555-555555555555", Type: "a.b.nuevo"}
	for _, e := range []events.Event{viejo, nuevo} {
		if err := Enqueue(ctx, pool, e.Type, e); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, "UPDATE platform.event_outbox SET published_at = now() - interval '3 days' WHERE id = $1", viejo.ID); err != nil {
		t.Fatal(err)
	}
	lock := func(c context.Context) (func(), bool) { return db.TryLeaderLock(c, pool, CellRelayLockKey) }
	pub := &publicadorFalso{}
	relay := NewRelay(pool, pub, zap.NewNop(), Options{Interval: 10 * time.Millisecond, Retention: 24 * time.Hour})
	correr := func() {
		c, cancel := context.WithTimeout(ctx, 80*time.Millisecond)
		defer cancel()
		relay.RunExclusive(c, lock)
	}

	// Otra instancia tiene el cerrojo: este rele no publica ni poda.
	release, ok := db.TryLeaderLock(ctx, pool, CellRelayLockKey)
	if !ok {
		t.Fatal("no se pudo tomar el cerrojo")
	}
	correr()
	var filas int
	_ = pool.QueryRow(ctx, "SELECT count(*) FROM platform.event_outbox").Scan(&filas)
	if len(pub.vistos) != 0 || filas != 2 {
		t.Fatalf("sin cerrojo no se vacia ni se poda: publicados=%v filas=%d", pub.vistos, filas)
	}
	release()

	correr()
	_ = pool.QueryRow(ctx, "SELECT count(*) FROM platform.event_outbox").Scan(&filas)
	if len(pub.vistos) != 1 || pub.vistos[0] != "a.b.nuevo|"+nuevo.ID || filas != 1 {
		t.Fatalf("con el cerrojo se publica lo pendiente y se poda lo viejo: publicados=%v filas=%d", pub.vistos, filas)
	}
}
