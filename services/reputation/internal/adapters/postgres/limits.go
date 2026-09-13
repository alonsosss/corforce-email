package postgres

import (
	"context"
	"errors"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/reputation/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const limitColumns = `tenant_id, class, hourly, daily, updated_by, updated_at`

// LimitRepository implementa ports.LimitRepository.
type LimitRepository struct {
	pool *db.ContextPool
}

func NewLimitRepository(pool *db.ContextPool) *LimitRepository {
	return &LimitRepository{pool: pool}
}

func scanOverride(row pgx.Row) (domain.LimitOverride, error) {
	var o domain.LimitOverride
	err := row.Scan(&o.TenantID, &o.Class, &o.Hourly, &o.Daily, &o.UpdatedBy, &o.UpdatedAt)
	return o, err
}

func (r *LimitRepository) Get(ctx context.Context, tenantID uuid.UUID, class domain.Class) (*domain.LimitOverride, error) {
	o, err := scanOverride(r.pool.QueryRow(ctx,
		`SELECT `+limitColumns+` FROM reputation.limit_overrides WHERE tenant_id = $1 AND class = $2`,
		tenantID, string(class)))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &o, nil
}

func (r *LimitRepository) List(ctx context.Context, tenantID uuid.UUID) ([]domain.LimitOverride, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+limitColumns+` FROM reputation.limit_overrides WHERE tenant_id = $1 ORDER BY class`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.LimitOverride
	for rows.Next() {
		o, err := scanOverride(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (r *LimitRepository) Upsert(ctx context.Context, o *domain.LimitOverride) error {
	return r.pool.QueryRow(ctx,
		`INSERT INTO reputation.limit_overrides (tenant_id, class, hourly, daily, updated_by)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (tenant_id, class) DO UPDATE
		    SET hourly = EXCLUDED.hourly, daily = EXCLUDED.daily, updated_by = EXCLUDED.updated_by
		 RETURNING updated_at`,
		o.TenantID, string(o.Class), o.Hourly, o.Daily, o.UpdatedBy,
	).Scan(&o.UpdatedAt)
}

func (r *LimitRepository) Delete(ctx context.Context, tenantID uuid.UUID, class domain.Class) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM reputation.limit_overrides WHERE tenant_id = $1 AND class = $2`, tenantID, string(class))
	return err
}
