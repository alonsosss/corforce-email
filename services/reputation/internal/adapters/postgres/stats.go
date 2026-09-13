// Package postgres persiste la reputacion en el esquema reputation de la base de la
// empresa. El pool o la transaccion salen del contexto (db.ContextPool), asi que todas las
// operaciones valen dentro de Transact.
package postgres

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/reputation/internal/domain"
	"github.com/google/uuid"
)

// StatsRepository implementa ports.StatsRepository.
type StatsRepository struct {
	pool *db.ContextPool
}

func NewStatsRepository(pool *db.ContextPool) *StatsRepository {
	return &StatsRepository{pool: pool}
}

func (r *StatsRepository) MarkProcessed(ctx context.Context, tenantID uuid.UUID, eventID string) (bool, error) {
	tag, err := r.pool.Exec(ctx,
		`INSERT INTO reputation.processed_events (event_id, tenant_id) VALUES ($1, $2)
		 ON CONFLICT (event_id) DO NOTHING`,
		eventID, tenantID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (r *StatsRepository) AddDaily(ctx context.Context, tenantID uuid.UUID, class domain.Class, day time.Time, d domain.Counts) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO reputation.daily_stats AS s (tenant_id, class, day, sent, bounced, complained)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (tenant_id, class, day) DO UPDATE
		    SET sent = s.sent + EXCLUDED.sent,
		        bounced = s.bounced + EXCLUDED.bounced,
		        complained = s.complained + EXCLUDED.complained`,
		tenantID, string(class), day, d.Sent, d.Bounced, d.Complained)
	return err
}

func (r *StatsRepository) WindowCounts(ctx context.Context, tenantID uuid.UUID, class domain.Class, from time.Time) (domain.Counts, error) {
	var c domain.Counts
	err := r.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(sent), 0)::bigint, COALESCE(SUM(bounced), 0)::bigint, COALESCE(SUM(complained), 0)::bigint
		   FROM reputation.daily_stats
		  WHERE tenant_id = $1 AND class = $2 AND day >= $3`,
		tenantID, string(class), from).Scan(&c.Sent, &c.Bounced, &c.Complained)
	return c, err
}

func (r *StatsRepository) WindowCountsByClass(ctx context.Context, tenantID uuid.UUID, from time.Time) (map[domain.Class]domain.Counts, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT class, SUM(sent)::bigint, SUM(bounced)::bigint, SUM(complained)::bigint
		   FROM reputation.daily_stats
		  WHERE tenant_id = $1 AND day >= $2
		  GROUP BY class`,
		tenantID, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[domain.Class]domain.Counts, len(domain.Classes()))
	for rows.Next() {
		var class domain.Class
		var c domain.Counts
		if err := rows.Scan(&class, &c.Sent, &c.Bounced, &c.Complained); err != nil {
			return nil, err
		}
		out[class] = c
	}
	return out, rows.Err()
}

func (r *StatsRepository) Prune(ctx context.Context, processedBefore, statsBefore time.Time) (int64, int64, error) {
	events, err := r.pool.Exec(ctx, `DELETE FROM reputation.processed_events WHERE processed_at < $1`, processedBefore)
	if err != nil {
		return 0, 0, err
	}
	days, err := r.pool.Exec(ctx, `DELETE FROM reputation.daily_stats WHERE day < $1`, statsBefore)
	if err != nil {
		return events.RowsAffected(), 0, err
	}
	return events.RowsAffected(), days.RowsAffected(), nil
}
