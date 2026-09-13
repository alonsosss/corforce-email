package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/alonsosss/corforce-email/services/billing/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// Los importes se leen como texto: numeric -> string -> decimal, sin pasar por float.
const planColumns = `id, code, name, description, currency, base_price::text, billing_period, status, created_at, updated_at`

type PlanRepository struct {
	db *Store
}

func NewPlanRepository(s *Store) *PlanRepository { return &PlanRepository{db: s} }

func scanPlan(row pgx.Row) (*domain.Plan, error) {
	var (
		p     domain.Plan
		price string
	)
	if err := row.Scan(&p.ID, &p.Code, &p.Name, &p.Description, &p.Currency, &price,
		&p.BillingPeriod, &p.Status, &p.CreatedAt, &p.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrPlanNotFound
		}
		return nil, err
	}
	d, err := decimal.NewFromString(price)
	if err != nil {
		return nil, fmt.Errorf("base_price ilegible del plan %s: %w", p.ID, err)
	}
	p.BasePrice = d
	return &p, nil
}

// attachLimits carga los limites de los planes dados en una sola consulta.
func (r *PlanRepository) attachLimits(ctx context.Context, plans []*domain.Plan) error {
	if len(plans) == 0 {
		return nil
	}
	ids := make([]string, len(plans))
	byID := make(map[uuid.UUID]*domain.Plan, len(plans))
	for i, p := range plans {
		ids[i] = p.ID.String()
		byID[p.ID] = p
		p.Limits = []domain.PlanLimit{}
	}
	rows, err := r.db.Query(ctx,
		`SELECT plan_id, resource, included, hard_limit, overage_unit_price::text
		   FROM billing.plan_limits WHERE plan_id = ANY($1::uuid[])`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			planID  uuid.UUID
			l       domain.PlanLimit
			overage *string
		)
		if err := rows.Scan(&planID, &l.Resource, &l.Included, &l.HardLimit, &overage); err != nil {
			return err
		}
		if overage != nil {
			d, err := decimal.NewFromString(*overage)
			if err != nil {
				return fmt.Errorf("overage_unit_price ilegible del plan %s: %w", planID, err)
			}
			l.OverageUnitPrice = &d
		}
		if p, ok := byID[planID]; ok {
			p.Limits = append(p.Limits, l)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, p := range plans {
		domain.SortLimits(p.Limits)
	}
	return nil
}

func (r *PlanRepository) one(ctx context.Context, sql string, arg interface{}) (*domain.Plan, error) {
	p, err := scanPlan(r.db.QueryRow(ctx, sql, arg))
	if err != nil {
		return nil, err
	}
	if err := r.attachLimits(ctx, []*domain.Plan{p}); err != nil {
		return nil, err
	}
	return p, nil
}

func (r *PlanRepository) Get(ctx context.Context, id uuid.UUID) (*domain.Plan, error) {
	return r.one(ctx, `SELECT `+planColumns+` FROM billing.plans WHERE id = $1`, id)
}

func (r *PlanRepository) GetForUpdate(ctx context.Context, id uuid.UUID) (*domain.Plan, error) {
	return r.one(ctx, `SELECT `+planColumns+` FROM billing.plans WHERE id = $1 FOR UPDATE`, id)
}

func (r *PlanRepository) GetByCode(ctx context.Context, code string) (*domain.Plan, error) {
	return r.one(ctx, `SELECT `+planColumns+` FROM billing.plans WHERE code = $1`, code)
}

func (r *PlanRepository) List(ctx context.Context, status domain.PlanStatus) ([]domain.Plan, error) {
	rows, err := r.db.Query(ctx,
		`SELECT `+planColumns+` FROM billing.plans WHERE ($1 = '' OR status = $1) ORDER BY created_at, code`,
		string(status))
	if err != nil {
		return nil, err
	}
	var plans []*domain.Plan
	for rows.Next() {
		p, err := scanPlan(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		plans = append(plans, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := r.attachLimits(ctx, plans); err != nil {
		return nil, err
	}
	out := make([]domain.Plan, 0, len(plans))
	for _, p := range plans {
		out = append(out, *p)
	}
	return out, nil
}

func (r *PlanRepository) Create(ctx context.Context, p *domain.Plan) error {
	err := r.db.QueryRow(ctx,
		`INSERT INTO billing.plans (code, name, description, currency, base_price, billing_period, status)
		 VALUES ($1, $2, $3, $4, $5::numeric, $6, $7)
		 RETURNING id, created_at, updated_at`,
		p.Code, p.Name, p.Description, p.Currency, p.BasePrice.String(), string(p.BillingPeriod), string(p.Status),
	).Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt)
	if isUniqueViolation(err) {
		return domain.ErrPlanCodeTaken
	}
	if err != nil {
		return err
	}
	return r.insertLimits(ctx, p.ID, p.Limits)
}

func (r *PlanRepository) insertLimits(ctx context.Context, planID uuid.UUID, limits []domain.PlanLimit) error {
	for _, l := range limits {
		var overage *string
		if l.OverageUnitPrice != nil {
			s := l.OverageUnitPrice.String()
			overage = &s
		}
		if _, err := r.db.Exec(ctx,
			`INSERT INTO billing.plan_limits (plan_id, resource, included, hard_limit, overage_unit_price)
			 VALUES ($1, $2, $3, $4, $5::numeric)`,
			planID, string(l.Resource), l.Included, l.HardLimit, overage); err != nil {
			return err
		}
	}
	return nil
}

func (r *PlanRepository) Update(ctx context.Context, p *domain.Plan, replaceLimits bool) error {
	err := r.db.QueryRow(ctx,
		`UPDATE billing.plans
		    SET name = $2, description = $3, currency = $4, base_price = $5::numeric,
		        billing_period = $6, status = $7
		  WHERE id = $1
		  RETURNING updated_at`,
		p.ID, p.Name, p.Description, p.Currency, p.BasePrice.String(), string(p.BillingPeriod), string(p.Status),
	).Scan(&p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrPlanNotFound
	}
	if err != nil || !replaceLimits {
		return err
	}
	if _, err := r.db.Exec(ctx, `DELETE FROM billing.plan_limits WHERE plan_id = $1`, p.ID); err != nil {
		return err
	}
	return r.insertLimits(ctx, p.ID, p.Limits)
}

func (r *PlanRepository) HasSubscriptions(ctx context.Context, planID uuid.UUID) (bool, error) {
	var exists bool
	err := r.db.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM billing.subscriptions WHERE plan_id = $1)`, planID).Scan(&exists)
	return exists, err
}
