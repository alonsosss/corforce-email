package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/reputation/internal/domain"
	"github.com/alonsosss/corforce-email/services/reputation/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// Las tasas viajan como texto en ambos sentidos: pgx no conoce decimal.Decimal y un
// float64 perderia la precision con la que se decide un estado.
const stateColumns = `tenant_id, class, state, reason, bounce_rate::text, complaint_rate::text, manual, changed_at, changed_by`

const historyColumns = `id, tenant_id, class, from_state, to_state, reason, bounce_rate::text, complaint_rate::text, manual, changed_by, created_at`

// StateRepository implementa ports.StateRepository.
type StateRepository struct {
	pool *db.ContextPool
}

func NewStateRepository(pool *db.ContextPool) *StateRepository {
	return &StateRepository{pool: pool}
}

func parseRates(bounce, complaint string) (decimal.Decimal, decimal.Decimal, error) {
	b, err := decimal.NewFromString(bounce)
	if err != nil {
		return decimal.Zero, decimal.Zero, fmt.Errorf("bounce_rate ilegible: %w", err)
	}
	c, err := decimal.NewFromString(complaint)
	if err != nil {
		return decimal.Zero, decimal.Zero, fmt.Errorf("complaint_rate ilegible: %w", err)
	}
	return b, c, nil
}

func scanRecord(row pgx.Row) (domain.Record, error) {
	var (
		rec               domain.Record
		bounce, complaint string
	)
	if err := row.Scan(&rec.TenantID, &rec.Class, &rec.State, &rec.Reason, &bounce, &complaint,
		&rec.Manual, &rec.ChangedAt, &rec.ChangedBy); err != nil {
		return domain.Record{}, err
	}
	var err error
	if rec.BounceRate, rec.ComplaintRate, err = parseRates(bounce, complaint); err != nil {
		return domain.Record{}, err
	}
	return rec, nil
}

func (r *StateRepository) Ensure(ctx context.Context, tenantID uuid.UUID, class domain.Class, now time.Time) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO reputation.states (tenant_id, class, state, reason, bounce_rate, complaint_rate, manual, changed_at)
		 VALUES ($1, $2, $3, $4, 0, 0, false, $5)
		 ON CONFLICT (tenant_id, class) DO NOTHING`,
		tenantID, string(class), string(domain.StateOK), domain.ReasonInitial, now)
	return err
}

func (r *StateRepository) GetForUpdate(ctx context.Context, tenantID uuid.UUID, class domain.Class) (domain.Record, error) {
	return scanRecord(r.pool.QueryRow(ctx,
		`SELECT `+stateColumns+` FROM reputation.states WHERE tenant_id = $1 AND class = $2 FOR UPDATE`,
		tenantID, string(class)))
}

func (r *StateRepository) Get(ctx context.Context, tenantID uuid.UUID, class domain.Class) (domain.Record, error) {
	rec, err := scanRecord(r.pool.QueryRow(ctx,
		`SELECT `+stateColumns+` FROM reputation.states WHERE tenant_id = $1 AND class = $2`,
		tenantID, string(class)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.DefaultRecord(tenantID, class), nil
	}
	return rec, err
}

func (r *StateRepository) List(ctx context.Context, tenantID uuid.UUID) ([]domain.Record, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+stateColumns+` FROM reputation.states WHERE tenant_id = $1 ORDER BY class`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Record
	for rows.Next() {
		rec, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (r *StateRepository) Save(ctx context.Context, rec domain.Record) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE reputation.states
		    SET state = $3, reason = $4, bounce_rate = $5::numeric, complaint_rate = $6::numeric,
		        manual = $7, changed_at = $8, changed_by = $9
		  WHERE tenant_id = $1 AND class = $2`,
		rec.TenantID, string(rec.Class), string(rec.State), rec.Reason,
		rec.BounceRate.StringFixed(domain.RateScale), rec.ComplaintRate.StringFixed(domain.RateScale),
		rec.Manual, rec.ChangedAt, rec.ChangedBy)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("reputation: la clase %s de la empresa %s no tiene fila de estado", rec.Class, rec.TenantID)
	}
	return nil
}

func (r *StateRepository) AppendHistory(ctx context.Context, c *domain.Change) error {
	createdAt := c.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	return r.pool.QueryRow(ctx,
		`INSERT INTO reputation.state_history
		        (tenant_id, class, from_state, to_state, reason, bounce_rate, complaint_rate, manual, changed_by, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6::numeric, $7::numeric, $8, $9, $10)
		 RETURNING id, created_at`,
		c.TenantID, string(c.Class), string(c.From), string(c.To), c.Reason,
		c.BounceRate.StringFixed(domain.RateScale), c.ComplaintRate.StringFixed(domain.RateScale),
		c.Manual, c.ChangedBy, createdAt,
	).Scan(&c.ID, &c.CreatedAt)
}

func (r *StateRepository) ListHistory(ctx context.Context, tenantID uuid.UUID, f ports.HistoryFilter) ([]domain.Change, int64, error) {
	where := `WHERE tenant_id = $1 AND ($2 = '' OR class = $2)`
	args := []interface{}{tenantID, string(f.Class)}

	var total int64
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM reputation.state_history `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.pool.Query(ctx,
		`SELECT `+historyColumns+` FROM reputation.state_history `+where+` ORDER BY created_at DESC, id LIMIT $3 OFFSET $4`,
		append(args, f.PerPage, (f.Page-1)*f.PerPage)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []domain.Change
	for rows.Next() {
		var (
			c                 domain.Change
			bounce, complaint string
		)
		if err := rows.Scan(&c.ID, &c.TenantID, &c.Class, &c.From, &c.To, &c.Reason, &bounce, &complaint,
			&c.Manual, &c.ChangedBy, &c.CreatedAt); err != nil {
			return nil, 0, err
		}
		if c.BounceRate, c.ComplaintRate, err = parseRates(bounce, complaint); err != nil {
			return nil, 0, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}
