package postgres

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/services/access-control/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type DenialRepo struct {
	pool *pgxpool.Pool
}

func NewDenialRepo(pool *pgxpool.Pool) *DenialRepo {
	return &DenialRepo{pool: pool}
}

func (r *DenialRepo) Record(ctx context.Context, d *domain.AccessDenial) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO access_control.access_denials (tenant_id, user_id, module, action, method, path, enforced)
 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		d.TenantID, d.UserID, d.Module, d.Action, d.Method, d.Path, d.Enforced,
	)
	return err
}

func (r *DenialRepo) List(ctx context.Context, tenantID uuid.UUID, limit int) ([]*domain.AccessDenial, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, tenant_id, user_id, module, action, method, path, enforced, created_at
 FROM access_control.access_denials
 WHERE tenant_id = $1
 ORDER BY created_at DESC
 LIMIT $2`, tenantID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]*domain.AccessDenial, 0)
	for rows.Next() {
		d := &domain.AccessDenial{}
		if err := rows.Scan(&d.ID, &d.TenantID, &d.UserID, &d.Module, &d.Action, &d.Method, &d.Path, &d.Enforced, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

func (r *DenialRepo) summaryBy(ctx context.Context, column string, tenantID uuid.UUID, since time.Time) ([]domain.DenialSummaryRow, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+column+`::text AS key, count(*) AS n
 FROM access_control.access_denials
 WHERE tenant_id = $1 AND created_at >= $2
 GROUP BY `+column+`
 ORDER BY n DESC
 LIMIT 20`, tenantID, since,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]domain.DenialSummaryRow, 0)
	for rows.Next() {
		var row domain.DenialSummaryRow
		if err := rows.Scan(&row.Key, &row.Count); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, nil
}

func (r *DenialRepo) SummaryByModule(ctx context.Context, tenantID uuid.UUID, since time.Time) ([]domain.DenialSummaryRow, error) {
	return r.summaryBy(ctx, "module", tenantID, since)
}

func (r *DenialRepo) SummaryByUser(ctx context.Context, tenantID uuid.UUID, since time.Time) ([]domain.DenialSummaryRow, error) {
	return r.summaryBy(ctx, "user_id", tenantID, since)
}

func (r *DenialRepo) Total(ctx context.Context, tenantID uuid.UUID, since time.Time) (int64, error) {
	var n int64
	err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM access_control.access_denials WHERE tenant_id = $1 AND created_at >= $2`,
		tenantID, since,
	).Scan(&n)
	return n, err
}
