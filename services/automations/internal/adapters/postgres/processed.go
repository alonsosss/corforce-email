package postgres

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/google/uuid"
)

type ProcessedRepository struct {
	pool *db.ContextPool
}

func NewProcessedRepository(pool *db.ContextPool) *ProcessedRepository {
	return &ProcessedRepository{pool: pool}
}

func (r *ProcessedRepository) IsProcessed(ctx context.Context, tenantID uuid.UUID, eventID string) (bool, error) {
	var ok bool
	err := r.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM automations.processed_events WHERE event_id = $1 AND tenant_id = $2)`,
		eventID, tenantID).Scan(&ok)
	return ok, err
}

func (r *ProcessedRepository) MarkProcessed(ctx context.Context, tenantID uuid.UUID, eventID string, at time.Time) (bool, error) {
	tag, err := r.pool.Exec(ctx,
		`INSERT INTO automations.processed_events (event_id, tenant_id, processed_at) VALUES ($1, $2, $3)
		 ON CONFLICT (event_id) DO NOTHING`,
		eventID, tenantID, at)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (r *ProcessedRepository) PruneProcessed(ctx context.Context, tenantID uuid.UUID, before time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM automations.processed_events WHERE tenant_id = $1 AND processed_at < $2`, tenantID, before)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
