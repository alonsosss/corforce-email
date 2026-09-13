//go:build integration

package outbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
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

// integrationEnv devuelve la variable de entorno que apunta a la infraestructura de la
// prueba. Sin ella la prueba se salta, salvo con INTEGRATION_REQUIRED=1 (make
// test-integration y CI): ahi es un fallo, porque un salto esconderia que no llego.
func integrationEnv(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		if os.Getenv("INTEGRATION_REQUIRED") == "1" {
			t.Fatalf("%s no definida con INTEGRATION_REQUIRED=1", name)
		}
		t.Skipf("%s no definida", name)
	}
	return v
}

// outboxDB abre OUTBOX_TEST_DSN, una base desechable, y le aplica dos veces la outbox de
// plataforma (la misma en registro, celda y empresa): la segunda pasada demuestra que
// tolera re-ejecutarse.
func outboxDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := integrationEnv(t, "OUTBOX_TEST_DSN")
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	_, file, _, _ := runtime.Caller(0)
	migration := filepath.Join(filepath.Dir(file), "..", "..", "migrations", "tenant", "canonical", "platform", "00_outbox.sql")
	sql, err := os.ReadFile(migration)
	if err != nil {
		t.Fatal(err)
	}
	for pass := 1; pass <= 2; pass++ {
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("outbox de plataforma, pasada %d: %v", pass, err)
		}
	}
	return pool
}

func TestReleVaciaLaOutboxYReintentaLoQueFalla(t *testing.T) {
	ctx := context.Background()
	pool := outboxDB(t)
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
	ctx := context.Background()
	pool := outboxDB(t)
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

	// Con el cerrojo libre se espera a la condicion y no a un plazo fijo: en un runner lento
	// con -race el plazo vencia entre el vaciado y la poda, que se cancelaba sin error. La
	// poda corre despues del vaciado en la misma vuelta, asi que una sola fila implica las dos.
	c, cancel := context.WithCancel(ctx)
	terminado := make(chan struct{})
	go func() {
		relay.RunExclusive(c, lock)
		close(terminado)
	}()
	limite := time.Now().Add(10 * time.Second)
	for {
		_ = pool.QueryRow(ctx, "SELECT count(*) FROM platform.event_outbox").Scan(&filas)
		if filas == 1 || time.Now().After(limite) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-terminado
	if len(pub.vistos) != 1 || pub.vistos[0] != "a.b.nuevo|"+nuevo.ID || filas != 1 {
		t.Fatalf("con el cerrojo se publica lo pendiente y se poda lo viejo: publicados=%v filas=%d", pub.vistos, filas)
	}
}
