package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/alonsosss/corforce-email/services/billing/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type UsageRepository struct {
	db *Store
}

func NewUsageRepository(s *Store) *UsageRepository { return &UsageRepository{db: s} }

// LockCounter crea el contador si falta y lo devuelve bloqueado: el DO UPDATE sin cambio
// real toma el cerrojo de fila en la misma sentencia.
func (r *UsageRepository) LockCounter(ctx context.Context, tenantID uuid.UUID, resource domain.Resource, periodStart time.Time) (*domain.Counter, error) {
	c := &domain.Counter{TenantID: tenantID, Resource: resource, PeriodStart: domain.Date(periodStart)}
	err := r.db.QueryRow(ctx,
		`INSERT INTO billing.usage_counters (tenant_id, resource, period_start)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (tenant_id, resource, period_start)
		 DO UPDATE SET quantity = billing.usage_counters.quantity
		 RETURNING quantity, limit_reached_period`,
		tenantID, string(resource), c.PeriodStart,
	).Scan(&c.Quantity, &c.LimitReachedPeriod)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (r *UsageRepository) SaveCounter(ctx context.Context, c *domain.Counter) error {
	_, err := r.db.Exec(ctx,
		`UPDATE billing.usage_counters SET quantity = $4, limit_reached_period = $5
		  WHERE tenant_id = $1 AND resource = $2 AND period_start = $3`,
		c.TenantID, string(c.Resource), c.PeriodStart, c.Quantity, c.LimitReachedPeriod)
	return err
}

func (r *UsageRepository) Quantity(ctx context.Context, tenantID uuid.UUID, resource domain.Resource, periodStart time.Time) (int64, error) {
	var q int64
	err := r.db.QueryRow(ctx,
		`SELECT quantity FROM billing.usage_counters
		  WHERE tenant_id = $1 AND resource = $2 AND period_start = $3`,
		tenantID, string(resource), domain.Date(periodStart)).Scan(&q)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return q, err
}

func (r *UsageRepository) Quantities(ctx context.Context, tenantID uuid.UUID, flowPeriodStart time.Time) (map[domain.Resource]int64, error) {
	flow := domain.Date(flowPeriodStart)
	rows, err := r.db.Query(ctx,
		`SELECT resource, period_start, quantity FROM billing.usage_counters
		  WHERE tenant_id = $1 AND period_start IN ($2, $3)`,
		tenantID, domain.StockPeriodStart, flow)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[domain.Resource]int64)
	for rows.Next() {
		var (
			resource domain.Resource
			period   time.Time
			quantity int64
		)
		if err := rows.Scan(&resource, &period, &quantity); err != nil {
			return nil, err
		}
		// El dominio decide que contador vale para cada recurso.
		if period.Equal(resource.CounterPeriod(flow)) {
			out[resource] = quantity
		}
	}
	return out, rows.Err()
}

// AddStockItem inserta la fuente y cuenta las OTRAS fuentes del objeto en la misma
// sentencia (la del CTE no ve su propia insercion). Lo serializa el cerrojo del contador.
func (r *UsageRepository) AddStockItem(ctx context.Context, tenantID uuid.UUID, resource domain.Resource, key, source string) (bool, error) {
	var inserted, others int64
	err := r.db.QueryRow(ctx,
		`WITH ins AS (
		     INSERT INTO billing.stock_items (tenant_id, resource, item_key, source)
		     VALUES ($1, $2, $3, $4)
		     ON CONFLICT DO NOTHING
		     RETURNING 1
		 )
		 SELECT (SELECT count(*) FROM ins),
		        (SELECT count(*) FROM billing.stock_items
		          WHERE tenant_id = $1 AND resource = $2 AND item_key = $3 AND source <> $4)`,
		tenantID, string(resource), key, source).Scan(&inserted, &others)
	if err != nil {
		return false, err
	}
	return inserted == 1 && others == 0, nil
}

func (r *UsageRepository) RemoveStockItem(ctx context.Context, tenantID uuid.UUID, resource domain.Resource, key, source string) (bool, error) {
	var deleted, others int64
	err := r.db.QueryRow(ctx,
		`WITH del AS (
		     DELETE FROM billing.stock_items
		      WHERE tenant_id = $1 AND resource = $2 AND item_key = $3 AND source = $4
		     RETURNING 1
		 )
		 SELECT (SELECT count(*) FROM del),
		        (SELECT count(*) FROM billing.stock_items
		          WHERE tenant_id = $1 AND resource = $2 AND item_key = $3 AND source <> $4)`,
		tenantID, string(resource), key, source).Scan(&deleted, &others)
	if err != nil {
		return false, err
	}
	return deleted == 1 && others == 0, nil
}

// Ledger implementa ports.EventLedger sobre billing.processed_events.
type Ledger struct {
	db *Store
}

func NewLedger(s *Store) *Ledger { return &Ledger{db: s} }

func (l *Ledger) MarkProcessed(ctx context.Context, eventID, subject string) (bool, error) {
	tag, err := l.db.Exec(ctx,
		`INSERT INTO billing.processed_events (event_id, subject) VALUES ($1, $2)
		 ON CONFLICT (event_id) DO NOTHING`, eventID, subject)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (l *Ledger) PruneProcessed(ctx context.Context, before time.Time) (int64, error) {
	tag, err := l.db.Exec(ctx, `DELETE FROM billing.processed_events WHERE processed_at < $1`, before)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
