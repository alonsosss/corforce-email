//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/ports"
	"github.com/google/uuid"
)

// Bajo el tope el total del listado es exacto; por encima se declara acotado, y un filtro que
// deja pocos vuelve a dar el total exacto.
func TestListado_TotalAcotadoSobreElTope(t *testing.T) {
	ctx, repo, pool := setup(t)
	tenant := uuid.New()
	total := int(db.PageCountCap) + 200
	if _, err := pool.Exec(context.Background(), `
INSERT INTO mail_migration.jobs (tenant_id, mailbox_id, mailbox_username, source_host, source_port, source_tls, source_username,
    status, requested_by, finished_at)
SELECT $1, gen_random_uuid(), 'buzon' || g, 'imap.origen.example', 993, 'ssl', 'u' || g,
       CASE WHEN g <= 30 THEN 'cancelled' ELSE 'succeeded' END, gen_random_uuid(), now()
  FROM generate_series(1, $2) g`, tenant, total); err != nil {
		t.Fatal(err)
	}
	jobs, got, err := repo.List(ctx, tenant, ports.ListFilter{}, ports.Page{Limit: 50, Offset: 100})
	if err != nil || len(jobs) != 50 || got != (ports.Total{Value: db.PageCountCap, Capped: true}) {
		t.Fatalf("sin filtro: %v %d %+v", err, len(jobs), got)
	}
	cancelled := domain.StatusCancelled
	jobs, got, err = repo.List(ctx, tenant, ports.ListFilter{Status: &cancelled}, ports.Page{Limit: 50})
	if err != nil || len(jobs) != 30 || got != (ports.Total{Value: 30}) {
		t.Fatalf("con un filtro que deja 30: %v %d %+v", err, len(jobs), got)
	}
}
