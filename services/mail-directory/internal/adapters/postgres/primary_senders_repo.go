package postgres

import (
	"context"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
)

// PrimarySenderRepo guarda mail.mailbox_primary_senders. Filtra por tenant_id ademas de la RLS del
// Transactor.
type PrimarySenderRepo struct{ pool *db.ContextPool }

func NewPrimarySenderRepo(pool *db.ContextPool) *PrimarySenderRepo {
	return &PrimarySenderRepo{pool: pool}
}

func (r *PrimarySenderRepo) ListByUsername(ctx context.Context, tenantID uuid.UUID, username string) ([]domain.PrimarySender, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, tenant_id, username, address, created_at FROM mail.mailbox_primary_senders
		  WHERE tenant_id = $1 AND username = $2 ORDER BY address`,
		tenantID, username)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []domain.PrimarySender{}
	for rows.Next() {
		var s domain.PrimarySender
		if err := rows.Scan(&s.ID, &s.TenantID, &s.Username, &s.Address, &s.CreatedAt); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, s)
	}
	return out, mapErr(rows.Err())
}

func (r *PrimarySenderRepo) Count(ctx context.Context, tenantID uuid.UUID, username string) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM mail.mailbox_primary_senders WHERE tenant_id = $1 AND username = $2`,
		tenantID, username).Scan(&n)
	return n, mapErr(err)
}

// Add inserta la direccion; si ya estaba, deja la fila como esta y devuelve la existente.
func (r *PrimarySenderRepo) Add(ctx context.Context, s *domain.PrimarySender) error {
	return mapErr(r.pool.QueryRow(ctx,
		`INSERT INTO mail.mailbox_primary_senders (id, tenant_id, username, address)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (username, address) DO UPDATE SET address = EXCLUDED.address
		 RETURNING id, created_at`,
		s.ID, s.TenantID, s.Username, s.Address).Scan(&s.ID, &s.CreatedAt))
}

func (r *PrimarySenderRepo) Remove(ctx context.Context, tenantID uuid.UUID, username, address string) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM mail.mailbox_primary_senders WHERE tenant_id = $1 AND username = $2 AND address = $3`,
		tenantID, username, address)
	return mapErr(err)
}

func (r *PrimarySenderRepo) DeleteByUsername(ctx context.Context, tenantID uuid.UUID, username string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM mail.mailbox_primary_senders WHERE tenant_id = $1 AND username = $2`, tenantID, username)
	return err
}
