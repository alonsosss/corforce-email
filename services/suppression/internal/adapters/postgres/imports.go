package postgres

import (
	"context"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/suppression/internal/domain"
	"github.com/google/uuid"
)

// ImportRepository implementa ports.ImportRepository: el rastro de las cargas masivas.
type ImportRepository struct {
	pool *db.ContextPool
}

func NewImportRepository(pool *db.ContextPool) *ImportRepository {
	return &ImportRepository{pool: pool}
}

func (r *ImportRepository) Create(ctx context.Context, imp *domain.Import) error {
	return r.pool.QueryRow(ctx,
		`INSERT INTO suppression.imports (tenant_id, total, added, skipped, created_by)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING id, created_at`,
		imp.TenantID, imp.Total, imp.Added, imp.Skipped, imp.CreatedBy,
	).Scan(&imp.ID, &imp.CreatedAt)
}

func (r *ImportRepository) List(ctx context.Context, tenantID uuid.UUID, page, perPage int) ([]domain.Import, int64, error) {
	var total int64
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM suppression.imports WHERE tenant_id = $1`, tenantID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.pool.Query(ctx,
		`SELECT id, tenant_id, total, added, skipped, created_by, created_at
		   FROM suppression.imports WHERE tenant_id = $1
		  ORDER BY created_at DESC, id LIMIT $2 OFFSET $3`,
		tenantID, perPage, (page-1)*perPage)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []domain.Import
	for rows.Next() {
		var imp domain.Import
		if err := rows.Scan(&imp.ID, &imp.TenantID, &imp.Total, &imp.Added, &imp.Skipped, &imp.CreatedBy, &imp.CreatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, imp)
	}
	return out, total, rows.Err()
}
