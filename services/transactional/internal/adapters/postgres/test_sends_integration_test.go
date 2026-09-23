//go:build integration

package postgres

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTestSendColumnAndInvariant(t *testing.T) {
	ctx, _, repo := setup(t)
	tenant := uuid.New()

	probe := marketingMessage(tenant, "ana@example.com")
	probe.Test, probe.TemplateID, probe.TemplateVersion = true, ptr(uuid.New()), ptr(3)
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

	// Una prueba es siempre el render de una plantilla para una sola persona: un cuerpo
	// crudo marcado como prueba se rechaza en cualquier clase.
	raw := newMessage(tenant, domain.StatusQueued, "luis@example.com")
	raw.Test = true
	if err := repo.InsertMessage(ctx, raw); err == nil {
		t.Fatal("la base rechaza un cuerpo crudo marcado como prueba")
	}
}

// Prueba de una version de plantilla (transactional/07): por el carril transaccional, o por el
// de marketing sin campana ni contacto. Un mensaje de marketing que no es prueba sigue
// exigiendo campana y contacto.
func TestTemplateTestSendInvariants(t *testing.T) {
	ctx, _, repo := setup(t)
	tenant := uuid.New()
	rendered := func(m *domain.Message) *domain.Message {
		m.TemplateID, m.TemplateVersion = ptr(uuid.New()), ptr(1)
		return m
	}

	transactional := rendered(newMessage(tenant, domain.StatusQueued, "luis@example.com"))
	transactional.Test = true
	if err := repo.InsertMessage(ctx, transactional); err != nil {
		t.Fatalf("prueba transaccional de una plantilla: %v", err)
	}
	marketing := rendered(newMessage(tenant, domain.StatusQueued, "ana@example.com"))
	marketing.Class, marketing.Unsubscribable, marketing.Test = domain.ClassMarketing, true, true
	if err := repo.InsertMessage(ctx, marketing); err != nil {
		t.Fatalf("prueba de marketing sin campana: %v", err)
	}

	noCampaign := rendered(newMessage(tenant, domain.StatusQueued, "eva@example.com"))
	noCampaign.Class, noCampaign.Unsubscribable = domain.ClassMarketing, true
	if err := repo.InsertMessage(ctx, noCampaign); err == nil {
		t.Fatal("un marketing real sin campana se rechaza")
	}
	noUnsubscribe := rendered(newMessage(tenant, domain.StatusQueued, "eva@example.com"))
	noUnsubscribe.Class, noUnsubscribe.Test = domain.ClassMarketing, true
	if err := repo.InsertMessage(ctx, noUnsubscribe); err == nil {
		t.Fatal("una prueba de marketing sin baja se rechaza")
	}
	two := rendered(newMessage(tenant, domain.StatusQueued, "eva@example.com", "sol@example.com"))
	two.Test = true
	if err := repo.InsertMessage(ctx, two); err == nil {
		t.Fatal("una prueba va a una sola persona")
	}

	plain := newMessage(tenant, domain.StatusQueued, "real@example.com")
	if err := repo.InsertMessage(ctx, plain); err != nil {
		t.Fatal(err)
	}
	n, err := repo.CountTestMessagesSince(ctx, tenant, time.Now().Add(-time.Hour))
	if err != nil || n != 2 {
		t.Fatalf("pruebas de la ultima hora: %d %v", n, err)
	}
	if n, _ := repo.CountTestMessagesSince(ctx, tenant, time.Now().Add(time.Hour)); n != 0 {
		t.Fatalf("fuera de la ventana no cuenta: %d", n)
	}
	stats, err := repo.CountByStatus(ctx, tenant, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil || len(stats) != 1 || stats[0].Count != 1 {
		t.Fatalf("las estadisticas no cuentan las pruebas: %+v %v", stats, err)
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
