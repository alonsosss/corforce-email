package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/alonsosss/corforce-email/services/billing/internal/domain"
	"github.com/alonsosss/corforce-email/services/billing/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const subscriptionColumns = `s.id, s.tenant_id, s.plan_id, p.code, s.status, s.current_period_start,
	s.current_period_end, s.anchor_day, s.trial_ends_at, s.cancel_at, s.created_at, s.updated_at`

const subscriptionFrom = ` FROM billing.subscriptions s JOIN billing.plans p ON p.id = s.plan_id`

type SubscriptionRepository struct {
	db *Store
}

func NewSubscriptionRepository(s *Store) *SubscriptionRepository {
	return &SubscriptionRepository{db: s}
}

func scanSubscription(row pgx.Row) (*domain.Subscription, error) {
	var s domain.Subscription
	if err := row.Scan(&s.ID, &s.TenantID, &s.PlanID, &s.PlanCode, &s.Status, &s.CurrentPeriodStart,
		&s.CurrentPeriodEnd, &s.AnchorDay, &s.TrialEndsAt, &s.CancelAt, &s.CreatedAt, &s.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrSubscriptionNotFound
		}
		return nil, err
	}
	return &s, nil
}

func (r *SubscriptionRepository) Create(ctx context.Context, s *domain.Subscription) (bool, error) {
	err := r.db.QueryRow(ctx,
		`INSERT INTO billing.subscriptions (tenant_id, plan_id, status, current_period_start,
		        current_period_end, anchor_day, trial_ends_at, cancel_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 ON CONFLICT (tenant_id) DO NOTHING
		 RETURNING id, created_at, updated_at`,
		s.TenantID, s.PlanID, string(s.Status), s.CurrentPeriodStart, s.CurrentPeriodEnd,
		s.AnchorDay, s.TrialEndsAt, s.CancelAt,
	).Scan(&s.ID, &s.CreatedAt, &s.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (r *SubscriptionRepository) GetByTenant(ctx context.Context, tenantID uuid.UUID) (*domain.Subscription, error) {
	return scanSubscription(r.db.QueryRow(ctx,
		`SELECT `+subscriptionColumns+subscriptionFrom+` WHERE s.tenant_id = $1`, tenantID))
}

func (r *SubscriptionRepository) GetByTenantForUpdate(ctx context.Context, tenantID uuid.UUID) (*domain.Subscription, error) {
	return scanSubscription(r.db.QueryRow(ctx,
		`SELECT `+subscriptionColumns+subscriptionFrom+` WHERE s.tenant_id = $1 FOR UPDATE OF s`, tenantID))
}

func (r *SubscriptionRepository) Update(ctx context.Context, s *domain.Subscription) error {
	err := r.db.QueryRow(ctx,
		`UPDATE billing.subscriptions
		    SET plan_id = $2, status = $3, current_period_start = $4, current_period_end = $5,
		        anchor_day = $6, trial_ends_at = $7, cancel_at = $8
		  WHERE id = $1
		  RETURNING updated_at`,
		s.ID, s.PlanID, string(s.Status), s.CurrentPeriodStart, s.CurrentPeriodEnd,
		s.AnchorDay, s.TrialEndsAt, s.CancelAt,
	).Scan(&s.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrSubscriptionNotFound
	}
	return err
}

func (r *SubscriptionRepository) List(ctx context.Context, f ports.SubscriptionFilter) ([]domain.Subscription, int64, error) {
	var total int64
	if err := r.db.QueryRow(ctx,
		`SELECT count(*) FROM billing.subscriptions WHERE ($1 = '' OR status = $1)`,
		string(f.Status)).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.Query(ctx,
		`SELECT `+subscriptionColumns+subscriptionFrom+`
		  WHERE ($1 = '' OR s.status = $1)
		  ORDER BY s.created_at DESC, s.id
		  LIMIT $2 OFFSET $3`,
		string(f.Status), f.PerPage, (f.Page-1)*f.PerPage)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []domain.Subscription{}
	for rows.Next() {
		s, err := scanSubscription(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *s)
	}
	return out, total, rows.Err()
}

func (r *SubscriptionRepository) ListDue(ctx context.Context, now time.Time, exclude []uuid.UUID, limit int) ([]uuid.UUID, error) {
	// Nunca NULL: tenant_id = ANY(NULL) es NULL y descartaria todas las filas.
	skip := make([]string, 0, len(exclude))
	for _, id := range exclude {
		skip = append(skip, id.String())
	}
	rows, err := r.db.Query(ctx,
		`SELECT tenant_id FROM billing.subscriptions
		  WHERE NOT (tenant_id = ANY($3::uuid[]))
		    AND ((status <> 'cancelled' AND current_period_end <= $1)
		      OR (status = 'trialing' AND trial_ends_at <= $2)
		      OR (status <> 'cancelled' AND cancel_at <= $2))
		  ORDER BY current_period_end, tenant_id
		  LIMIT $4`,
		domain.Date(now), now, skip, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
