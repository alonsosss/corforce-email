//go:build integration

package postgres

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTestSendColumnAndInvariant(t *testing.T) {
	ctx, _, repo := setup(t)
	tenant := uuid.New()

	probe := marketingMessage(tenant, "ana@example.com")
	probe.Test = true
	if err := repo.InsertMessage(ctx, probe); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetMessage(ctx, tenant, probe.ID)
	if err != nil || !got.Test {
		t.Fatalf("la marca de prueba sobrevive al viaje: %+v %v", got, err)
	}
	attr, err := repo.GetAttribution(ctx, tenant, probe.ID)
	if err != nil || !attr.Test {
		t.Fatalf("la atribucion lleva la marca: %+v %v", attr, err)
	}

	campaign := marketingMessage(tenant, "eva@example.com")
	if err := repo.InsertMessage(ctx, campaign); err != nil {
		t.Fatal(err)
	}
	if a, _ := repo.GetAttribution(ctx, tenant, campaign.ID); a.Test {
		t.Fatal("un mensaje sin marca no es de prueba")
	}

	// Solo el carril de marketing puede marcar una prueba.
	transactional := newMessage(tenant, domain.StatusQueued, "luis@example.com")
	transactional.Test = true
	if err := repo.InsertMessage(ctx, transactional); err == nil {
		t.Fatal("la base rechaza un transaccional marcado como prueba")
	}
}

// La migracion marca, una sola vez, los envios de prueba anteriores a la columna: los lotes
// de marketing con la etiqueta test=true. Un transaccional con la misma etiqueta no se
// marca. Corre en una base temporal que crea y borra la propia prueba.
func TestTestSendsBackfill(t *testing.T) {
	dsn := os.Getenv("TRANSACTIONAL_TEST_DSN")
	if dsn == "" {
		t.Skip("TRANSACTIONAL_TEST_DSN no definida")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := "transactional_backfill_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec(ctx, `CREATE DATABASE `+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(ctx, `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`); err != nil {
			t.Errorf("borrar la base temporal %s: %v", name, err)
		}
		admin.Close(ctx)
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	applyMigrations(t, ctx, pool,
		"migrations/tenant/canonical/platform/00_outbox.sql",
		"migrations/tenant/canonical/transactional/01_transactional.sql",
		"migrations/tenant/canonical/transactional/02_marketing_lane.sql")

	tenant := uuid.New()
	probe, campaign, transactional := uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO transactional.messages
		(id, tenant_id, from_email, "to", subject, html, tags, unsubscribable, class, campaign_id, contact_id)
		VALUES
		($1, $4, 'news@shop.example.com', '[{"email":"ana@example.com"}]', 'Prueba', '<p>x</p>', '{"test":"true"}', true, 'marketing', $5, $6),
		($2, $4, 'news@shop.example.com', '[{"email":"eva@example.com"}]', 'Otono', '<p>x</p>', '{"campaign":"otono"}', true, 'marketing', $5, $6),
		($3, $4, 'no-reply@shop.example.com', '[{"email":"luis@example.com"}]', 'Pedido', '<p>x</p>', '{"test":"true"}', false, 'transactional', NULL, NULL)`,
		probe, campaign, transactional, tenant, uuid.New(), uuid.New()); err != nil {
		t.Fatal(err)
	}

	migration := "migrations/tenant/canonical/transactional/03_test_sends.sql"
	applyMigrations(t, ctx, pool, migration, migration)

	rows, err := pool.Query(ctx, `SELECT id, is_test FROM transactional.messages WHERE tenant_id = $1`, tenant)
	if err != nil {
		t.Fatal(err)
	}
	marked := map[uuid.UUID]bool{}
	for rows.Next() {
		var id uuid.UUID
		var isTest bool
		if err := rows.Scan(&id, &isTest); err != nil {
			t.Fatal(err)
		}
		marked[id] = isTest
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(marked) != 3 || !marked[probe] || marked[campaign] || marked[transactional] {
		t.Fatalf("relleno de is_test: %v", marked)
	}
	var constraints int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint WHERE conname = 'messages_test_marketing_check'`).Scan(&constraints); err != nil || constraints != 1 {
		t.Fatalf("restriccion de la marca (una sola vez): %d %v", constraints, err)
	}
}
