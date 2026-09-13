//go:build integration

package postgres

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/outbox"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/app"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

type busFalso struct {
	mu     sync.Mutex
	vistos []events.Event
}

func (b *busFalso) PublishPersistent(_ string, evt events.Event) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.vistos = append(b.vistos, evt)
	return nil
}

// El alta de un buzon bajo TransactRLS (rol mail_app) deja la fila de negocio y la de la
// outbox en la misma transaccion; un fallo al encolar no deja ninguna; y el rele entrega
// el evento con el id de la fila, que es la clave de deduplicacion de los consumidores.
func TestOutboxEnLaMismaTransaccion(t *testing.T) {
	dsn := integrationEnv(t, "MAIL_DIRECTORY_TEST_DSN")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	applyCellMigrations(t, ctx, pool)

	uc := newUseCase(&db.ContextPool{})
	tenant := uuid.New()
	dom := "ob-" + strings.Split(uuid.New().String(), "-")[0] + ".example"
	t.Cleanup(func() { cleanup(t, pool, dom, dom, tenant) })
	// Con usuario en el contexto TransactRLS cambia al rol mail_app: el camino real.
	actx := middleware.WithIdentity(db.WithPool(ctx, pool), uuid.New().String(), tenant.String())

	if _, err := uc.SetDomainActivation(actx, tenant, dom, true); err != nil {
		t.Fatalf("activar: %v", err)
	}
	mb, err := uc.CreateMailbox(actx, tenant, app.CreateMailboxRequest{LocalPart: "ana", Domain: dom, Password: "contrasena-de-prueba-1"})
	if err != nil {
		t.Fatalf("alta de buzon bajo mail_app: %v", err)
	}
	rows := func(username string) (mailboxes, queued int) {
		t.Helper()
		if err := pool.QueryRow(ctx, `
			SELECT (SELECT count(*) FROM mail.mailboxes WHERE username = $1),
			       (SELECT count(*) FROM platform.event_outbox
			         WHERE subject = 'mail.mailbox.created' AND tenant_id = $2
			           AND payload->'data'->>'username' = $1 AND payload->>'id' = id::text)`,
			username, tenant).Scan(&mailboxes, &queued); err != nil {
			t.Fatal(err)
		}
		return mailboxes, queued
	}
	if m, q := rows(mb.Username); m != 1 || q != 1 {
		t.Fatalf("alta confirmada: buzones=%d eventos=%d (quiero 1 y 1)", m, q)
	}
	var typ, src, envTenant string
	if err := pool.QueryRow(ctx, `SELECT payload->>'type', payload->>'source', payload->>'tenant_id' FROM platform.event_outbox
		WHERE subject = 'mail.mailbox.created' AND payload->'data'->>'username' = $1`, mb.Username).Scan(&typ, &src, &envTenant); err != nil {
		t.Fatal(err)
	}
	if typ != "mail.mailbox.created" || src != "mail-directory" || envTenant != tenant.String() {
		t.Fatalf("sobre del evento: type=%q source=%q tenant=%q", typ, src, envTenant)
	}

	// Fallo inyectado en la base: sin permiso de INSERT sobre la outbox, Enqueue falla
	// DENTRO de la transaccion y el buzon tampoco se crea.
	if _, err := pool.Exec(ctx, `REVOKE INSERT ON platform.event_outbox FROM mail_app`); err != nil {
		t.Fatal(err)
	}
	grant := filepath.Join(repoRoot(), "migrations/cell/canonical/mail-directory/05_outbox_grants.sql")
	t.Cleanup(func() { _ = execFile(context.Background(), pool, grant) })
	luis := "luis@" + dom
	if _, err := uc.CreateMailbox(actx, tenant, app.CreateMailboxRequest{LocalPart: "luis", Domain: dom, Password: "contrasena-de-prueba-1"}); err == nil {
		t.Fatal("sin outbox el alta debe fallar")
	}
	if m, q := rows(luis); m != 0 || q != 0 {
		t.Fatalf("un fallo al encolar no deja nada: buzones=%d eventos=%d", m, q)
	}
	// Re-aplicar la migracion restituye el permiso: el alta vuelve a funcionar.
	if err := execFile(ctx, pool, grant); err != nil {
		t.Fatal(err)
	}
	if _, err := uc.CreateMailbox(actx, tenant, app.CreateMailboxRequest{LocalPart: "luis", Domain: dom, Password: "contrasena-de-prueba-1"}); err != nil {
		t.Fatalf("alta tras restituir el permiso: %v", err)
	}
	if m, q := rows(luis); m != 1 || q != 1 {
		t.Fatalf("alta tras restituir: buzones=%d eventos=%d", m, q)
	}

	// El rele entrega el evento con el id de la fila y el payload que leen los consumidores.
	var rowID string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM platform.event_outbox WHERE subject = 'mail.mailbox.created'
		AND payload->'data'->>'username' = $1`, mb.Username).Scan(&rowID); err != nil {
		t.Fatal(err)
	}
	bus := &busFalso{}
	relay := outbox.NewRelay(pool, bus, zap.NewNop(), outbox.Options{Batch: 500})
	for {
		n, err := relay.Drain(ctx)
		if err != nil {
			t.Fatalf("vaciar: %v", err)
		}
		if n < 500 {
			break
		}
	}
	var delivered *events.Event
	for i := range bus.vistos {
		if bus.vistos[i].ID == rowID {
			delivered = &bus.vistos[i]
		}
	}
	if delivered == nil {
		t.Fatalf("el rele no entrego la fila %s", rowID)
	}
	data, _ := delivered.Data.(map[string]interface{})
	if delivered.Type != "mail.mailbox.created" || delivered.TenantID != tenant.String() || data["username"] != mb.Username || data["tenant_id"] != tenant.String() {
		t.Fatalf("evento entregado: %+v", delivered)
	}
	var pending int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM platform.event_outbox WHERE id = $1 AND published_at IS NULL`, rowID).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("la fila entregada queda marcada: pendientes=%d %v", pending, err)
	}
}
