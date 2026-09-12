//go:build integration

package postgres

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/suppression/internal/domain"
	"github.com/alonsosss/corforce-email/services/suppression/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SUPPRESSION_TEST_DSN apunta a una base con migrations/tenant/canonical/platform/00_outbox.sql
// y migrations/tenant/canonical/suppression/01_suppression.sql aplicadas.
func testPool(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	dsn := os.Getenv("SUPPRESSION_TEST_DSN")
	if dsn == "" {
		t.Skip("SUPPRESSION_TEST_DSN no definido")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool, db.WithPool(ctx, pool)
}

func TestRepositorioEntradas(t *testing.T) {
	_, ctx := testPool(t)
	repo := NewRepository(&db.ContextPool{})
	tenant := uuid.New()

	e := &domain.Entry{TenantID: tenant, Email: "ana@example.com", Reason: domain.ReasonManual, Source: "api", Detail: "prueba"}
	if err := repo.Insert(ctx, e); err != nil {
		t.Fatal(err)
	}
	if e.ID == uuid.Nil || e.CreatedAt.IsZero() {
		t.Fatalf("insert no devolvio id/created_at: %+v", e)
	}
	if err := repo.Insert(ctx, &domain.Entry{TenantID: tenant, Email: "ana@example.com", Reason: domain.ReasonManual}); !errors.Is(err, domain.ErrEntryAlreadyExists) {
		t.Fatalf("duplicado: se esperaba ErrEntryAlreadyExists, hubo %v", err)
	}
	// El CHECK de minusculas protege aunque el codigo no normalice.
	if err := repo.Insert(ctx, &domain.Entry{TenantID: tenant, Email: "MAYUS@example.com", Reason: domain.ReasonManual}); err == nil {
		t.Fatal("el CHECK de minusculas debe rechazar la fila")
	}
	// expires_at solo en manual.
	exp := time.Now().Add(time.Hour)
	if err := repo.Insert(ctx, &domain.Entry{TenantID: tenant, Email: "rebote@example.com", Reason: domain.ReasonHardBounce, ExpiresAt: &exp}); err == nil {
		t.Fatal("expires_at con hard_bounce debe violar el CHECK")
	}

	msg := uuid.New()
	e.Reason, e.Source, e.MessageID = domain.ReasonHardBounce, "ses", &msg
	if err := repo.Update(ctx, e); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetByID(ctx, tenant, e.ID)
	if err != nil || got.Reason != domain.ReasonHardBounce || got.MessageID == nil || *got.MessageID != msg {
		t.Fatalf("update no persistio: %+v err=%v", got, err)
	}
	if !got.UpdatedAt.After(got.CreatedAt) {
		t.Fatalf("el trigger de updated_at no corrio: created=%s updated=%s", got.CreatedAt, got.UpdatedAt)
	}
	if _, err := repo.GetByID(ctx, uuid.New(), e.ID); !errors.Is(err, domain.ErrEntryNotFound) {
		t.Fatalf("otra empresa no debe ver la fila: %v", err)
	}

	added, err := repo.InsertMissing(ctx, tenant, []string{"ana@example.com", "b@example.com", "c@example.com"}, domain.ReasonManual, "import", "")
	if err != nil || len(added) != 2 {
		t.Fatalf("InsertMissing: added=%d err=%v", len(added), err)
	}

	found, err := repo.FindByEmails(ctx, tenant, []string{"ana@example.com", "c@example.com", "nadie@example.com"})
	if err != nil || len(found) != 2 {
		t.Fatalf("FindByEmails: %d err=%v", len(found), err)
	}

	list, total, err := repo.List(ctx, tenant, ports.ListFilter{Search: "%", Page: 1, PerPage: 10})
	if err != nil || total != 0 || len(list) != 0 {
		t.Fatalf("el comodin de LIKE debe escaparse: total=%d err=%v", total, err)
	}
	list, total, err = repo.List(ctx, tenant, ports.ListFilter{Reason: domain.ReasonManual, Search: "EXAMPLE", Page: 1, PerPage: 1})
	if err != nil || total != 2 || len(list) != 1 {
		t.Fatalf("List filtrado: total=%d n=%d err=%v", total, len(list), err)
	}

	counts, err := repo.CountByReason(ctx, tenant, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	sum := int64(0)
	for _, c := range counts {
		sum += c.Count
	}
	if sum != 3 {
		t.Fatalf("CountByReason: %+v", counts)
	}

	if err := repo.Delete(ctx, tenant, e.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.Delete(ctx, tenant, e.ID); !errors.Is(err, domain.ErrEntryNotFound) {
		t.Fatalf("segundo delete: %v", err)
	}
}

func TestRepositorioBloqueaLaFilaEnTransaccion(t *testing.T) {
	_, ctx := testPool(t)
	pool := &db.ContextPool{}
	repo := NewRepository(pool)
	tenant := uuid.New()
	if err := repo.Insert(ctx, &domain.Entry{TenantID: tenant, Email: "lock@example.com", Reason: domain.ReasonManual}); err != nil {
		t.Fatal(err)
	}
	err := pool.Transact(ctx, func(txCtx context.Context) error {
		e, err := repo.GetByEmailForUpdate(txCtx, tenant, "lock@example.com")
		if err != nil {
			return err
		}
		e.Reason = domain.ReasonComplaint
		return repo.Update(txCtx, e)
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetByEmailForUpdate(ctx, tenant, "lock@example.com")
	if err != nil || got.Reason != domain.ReasonComplaint {
		t.Fatalf("la transaccion no confirmo: %+v err=%v", got, err)
	}
}

func TestRepositorioCargas(t *testing.T) {
	_, ctx := testPool(t)
	repo := NewImportRepository(&db.ContextPool{})
	tenant := uuid.New()
	imp := &domain.Import{TenantID: tenant, Total: 5, Added: 3, Skipped: 2, CreatedBy: uuid.New()}
	if err := repo.Create(ctx, imp); err != nil {
		t.Fatal(err)
	}
	if err := repo.Create(ctx, &domain.Import{TenantID: tenant, Total: 5, Added: 3, Skipped: 1, CreatedBy: uuid.New()}); err == nil {
		t.Fatal("el CHECK de conteos debe rechazar added+skipped != total")
	}
	list, total, err := repo.List(ctx, tenant, 1, 10)
	if err != nil || total != 1 || len(list) != 1 || list[0].ID != imp.ID {
		t.Fatalf("List: total=%d n=%d err=%v", total, len(list), err)
	}
}
