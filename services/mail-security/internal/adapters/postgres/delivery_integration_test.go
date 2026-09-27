//go:build integration

package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Registro de entregas (09_delivery_events.sql): idempotencia, RLS por empresa, filtros y poda.
func TestIntegracionRegistroDeEntregas(t *testing.T) {
	dsn := os.Getenv("MAIL_SECURITY_TEST_DSN")
	if dsn == "" {
		t.Skip("MAIL_SECURITY_TEST_DSN no definido")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	applyMigrations(t, ctx, pool, repoRoot(t))
	if _, err := pool.Exec(ctx, `DELETE FROM mail_security.delivery_events`); err != nil {
		t.Fatal(err)
	}
	ctxPool := &db.ContextPool{}
	engineCtx := db.WithPool(ctx, pool)
	repo := NewDeliveryLogRepository(ctxPool)

	now := time.Now().UTC().Truncate(time.Second)
	event := func(tenant uuid.UUID, key, status, rcpt string, at time.Time) *domain.DeliveryEvent {
		return &domain.DeliveryEvent{
			ID: uuid.New(), TenantID: tenant, Direction: domain.DirectionOutbound, QueueID: "ABC123",
			Sender: "ana@acme.com", Recipient: rcpt, Status: status, DSN: "5.1.1", Reason: "550 User unknown",
			DelaySeconds: "1.25", OccurredAt: at, EventKey: key,
		}
	}
	for _, e := range []*domain.DeliveryEvent{
		event(tenantA, "k1", domain.DeliverySent, "eva@gmail.com", now.Add(-time.Hour)),
		event(tenantA, "k2", domain.DeliveryBounced, "nadie@empresa.example", now),
		event(tenantB, "k1", domain.DeliverySent, "eva@gmail.com", now),
		event(tenantA, "viejo", domain.DeliverySent, "eva@gmail.com", now.Add(-100*24*time.Hour)),
	} {
		if ok, err := repo.Insert(engineCtx, e); err != nil || !ok {
			t.Fatalf("insertar %s: %v %v", e.EventKey, ok, err)
		}
	}
	if ok, err := repo.Insert(engineCtx, event(tenantA, "k1", domain.DeliverySent, "eva@gmail.com", now)); err != nil || ok {
		t.Fatalf("la misma huella en la misma empresa no se guarda dos veces: %v %v", ok, err)
	}

	list := func(tenant uuid.UUID, f domain.DeliveryFilter) ([]domain.DeliveryEvent, int64) {
		t.Helper()
		f.Normalize()
		var items []domain.DeliveryEvent
		var total int64
		if err := ctxPool.TransactRLS(adminCtx(pool, tenant), func(ctx context.Context) error {
			var err error
			items, total, err = repo.List(ctx, tenant, f)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return items, total
	}
	items, total := list(tenantA, domain.DeliveryFilter{})
	if total != 3 || items[0].EventKey != "" || items[0].Status != domain.DeliveryBounced || items[0].DelaySeconds != "1.250" {
		t.Fatalf("empresa A, del mas reciente al mas antiguo: %d %+v", total, items)
	}
	if items, _ := list(tenantA, domain.DeliveryFilter{Status: domain.DeliveryBounced, Address: "nadie@empresa.example"}); len(items) != 1 {
		t.Errorf("filtro por estado y direccion: %+v", items)
	}
	since := now.Add(-2 * time.Hour)
	if _, total := list(tenantA, domain.DeliveryFilter{DateFrom: &since}); total != 2 {
		t.Errorf("filtro por fecha: %d", total)
	}
	// Bajo RLS la empresa A no ve la fila de B aunque pida el id de B en el filtro.
	var leaked int
	if err := ctxPool.TransactRLS(adminCtx(pool, tenantA), func(ctx context.Context) error {
		return ctxPool.QueryRow(ctx, `SELECT count(*) FROM mail_security.delivery_events WHERE tenant_id = $1`, tenantB).Scan(&leaked)
	}); err != nil || leaked != 0 {
		t.Fatalf("RLS: %d filas de otra empresa, %v", leaked, err)
	}
	if err := ctxPool.TransactRLS(adminCtx(pool, tenantA), func(ctx context.Context) error {
		_, err := ctxPool.Exec(ctx, `DELETE FROM mail_security.delivery_events`)
		return err
	}); err == nil {
		t.Fatal("mail_app solo lee el registro")
	}

	pruned, err := repo.PruneBefore(engineCtx, now.Add(-90*24*time.Hour), 100)
	if err != nil || pruned != 1 {
		t.Fatalf("poda: %d %v", pruned, err)
	}
}
