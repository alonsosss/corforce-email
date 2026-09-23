package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/campaigns/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const phaseColumns = `id, tenant_id, campaign_id, kind, key, ordinal, variant, slot_at, not_before, status,
	targeted, accepted, suppressed, started_at, completed_at, created_at, updated_at`

type PhaseRepository struct {
	pool *db.ContextPool
}

func NewPhaseRepository(pool *db.ContextPool) *PhaseRepository {
	return &PhaseRepository{pool: pool}
}

func scanPhase(row pgx.Row) (*domain.Phase, error) {
	var (
		p            domain.Phase
		kind, status string
	)
	if err := row.Scan(&p.ID, &p.TenantID, &p.CampaignID, &kind, &p.Key, &p.Ordinal, &p.Variant, &p.SlotAt,
		&p.NotBefore, &status, &p.Targeted, &p.Accepted, &p.Suppressed, &p.StartedAt, &p.CompletedAt,
		&p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	p.Kind = domain.PhaseKind(kind)
	p.Status = domain.PhaseStatus(status)
	return &p, nil
}

func (r *PhaseRepository) List(ctx context.Context, tenantID, campaignID uuid.UUID) ([]domain.Phase, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+phaseColumns+` FROM campaigns.phases
		  WHERE tenant_id = $1 AND campaign_id = $2
		  ORDER BY ordinal, slot_at NULLS FIRST, created_at, id`,
		tenantID, campaignID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Phase
	for rows.Next() {
		p, err := scanPhase(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (r *PhaseRepository) Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.Phase, error) {
	p, err := scanPhase(r.pool.QueryRow(ctx,
		`SELECT `+phaseColumns+` FROM campaigns.phases WHERE tenant_id = $1 AND id = $2`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("fase %s inexistente", id)
	}
	return p, err
}

func (r *PhaseRepository) Insert(ctx context.Context, p *domain.Phase) (bool, error) {
	err := r.pool.QueryRow(ctx,
		`INSERT INTO campaigns.phases (id, tenant_id, campaign_id, kind, key, ordinal, variant, slot_at, not_before, status)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 ON CONFLICT (campaign_id, key) DO NOTHING
		 RETURNING created_at, updated_at`,
		p.ID, p.TenantID, p.CampaignID, string(p.Kind), p.Key, p.Ordinal, p.Variant, p.SlotAt, p.NotBefore, string(p.Status),
	).Scan(&p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (r *PhaseRepository) Update(ctx context.Context, p *domain.Phase) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE campaigns.phases SET status = $3, not_before = $4, started_at = $5, completed_at = $6
		  WHERE tenant_id = $1 AND id = $2`,
		p.TenantID, p.ID, string(p.Status), p.NotBefore, p.StartedAt, p.CompletedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("fase %s inexistente", p.ID)
	}
	return nil
}

func (r *PhaseRepository) AddTotals(ctx context.Context, tenantID, id uuid.UUID, targeted, accepted, suppressed int) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE campaigns.phases
		    SET targeted = targeted + $3, accepted = accepted + $4, suppressed = suppressed + $5
		  WHERE tenant_id = $1 AND id = $2`,
		tenantID, id, targeted, accepted, suppressed)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("fase %s inexistente", id)
	}
	return nil
}
