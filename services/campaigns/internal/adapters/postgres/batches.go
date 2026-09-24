package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/campaigns/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// batchColumns deja fuera la pagina: lleva datos de contacto y solo la necesita el
// orquestador al reintentar (Pending).
const batchColumns = `id, tenant_id, campaign_id, phase_id, seq, cursor_in, cursor_out, recipients, status,
	accepted, suppressed, attempts, last_error, leased_until, lease_token, created_at, updated_at`

type BatchRepository struct {
	pool *db.ContextPool
}

func NewBatchRepository(pool *db.ContextPool) *BatchRepository {
	return &BatchRepository{pool: pool}
}

func scanBatch(row pgx.Row, withPage bool) (*domain.Batch, error) {
	var (
		b      domain.Batch
		status string
		page   []byte
	)
	dest := []interface{}{&b.ID, &b.TenantID, &b.CampaignID, &b.PhaseID, &b.Seq, &b.CursorIn, &b.CursorOut, &b.Recipients,
		&status, &b.Accepted, &b.Suppressed, &b.Attempts, &b.LastError, &b.LeasedUntil, &b.LeaseToken,
		&b.CreatedAt, &b.UpdatedAt}
	if withPage {
		dest = append(dest, &page)
	}
	if err := row.Scan(dest...); err != nil {
		return nil, err
	}
	b.Status = domain.BatchStatus(status)
	if page != nil {
		b.PageFetched = true
		if err := json.Unmarshal(page, &b.Page); err != nil {
			return nil, fmt.Errorf("pagina ilegible en el lote %s: %w", b.ID, err)
		}
	}
	return &b, nil
}

// optionalBatch traduce "no hay fila" a nil, nil.
func optionalBatch(b *domain.Batch, err error) (*domain.Batch, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return b, err
}

func (r *BatchRepository) Pending(ctx context.Context, tenantID, campaignID uuid.UUID) (*domain.Batch, error) {
	return optionalBatch(scanBatch(r.pool.QueryRow(ctx,
		`SELECT `+batchColumns+`, page FROM campaigns.batches
		  WHERE tenant_id = $1 AND campaign_id = $2 AND status = $3`,
		tenantID, campaignID, string(domain.BatchPending)), true))
}

func (r *BatchRepository) Last(ctx context.Context, tenantID, campaignID uuid.UUID) (*domain.Batch, error) {
	return optionalBatch(scanBatch(r.pool.QueryRow(ctx,
		`SELECT `+batchColumns+` FROM campaigns.batches
		  WHERE tenant_id = $1 AND campaign_id = $2 ORDER BY seq DESC LIMIT 1`,
		tenantID, campaignID), false))
}

func (r *BatchRepository) Insert(ctx context.Context, b *domain.Batch) error {
	return r.pool.QueryRow(ctx,
		`INSERT INTO campaigns.batches (id, tenant_id, campaign_id, phase_id, seq, cursor_in, status, leased_until, lease_token)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		 RETURNING created_at, updated_at`,
		b.ID, b.TenantID, b.CampaignID, b.PhaseID, b.Seq, b.CursorIn, string(b.Status), b.LeasedUntil, b.LeaseToken,
	).Scan(&b.CreatedAt, &b.UpdatedAt)
}

func (r *BatchRepository) Lease(ctx context.Context, b *domain.Batch) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE campaigns.batches SET leased_until = $3, lease_token = $4
		  WHERE tenant_id = $1 AND id = $2 AND status = $5`,
		b.TenantID, b.ID, b.LeasedUntil, b.LeaseToken, string(domain.BatchPending))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("el lote %s ya no está pendiente", b.ID)
	}
	return nil
}

// SavePage exige que la campana siga en envio: si una persona la pauso o cancelo
// mientras se pedia la pagina, el lote no llega a enviarse.
func (r *BatchRepository) SavePage(ctx context.Context, b *domain.Batch) (bool, error) {
	recipients := b.Page
	if recipients == nil {
		// Una pagina sin nadie de la fase es una lista vacia, no null: la restriccion de la
		// tabla exige un array.
		recipients = []domain.Recipient{}
	}
	page, err := json.Marshal(recipients)
	if err != nil {
		return false, err
	}
	tag, err := r.pool.Exec(ctx,
		`UPDATE campaigns.batches bt
		    SET page = $4, cursor_out = $5, recipients = $6
		  WHERE bt.tenant_id = $1 AND bt.id = $2 AND bt.lease_token = $3 AND bt.status = $7 AND bt.page IS NULL
		    AND EXISTS (SELECT 1 FROM campaigns.campaigns c
		                 WHERE c.tenant_id = bt.tenant_id AND c.id = bt.campaign_id AND c.status = $8)`,
		b.TenantID, b.ID, b.LeaseToken, page, b.CursorOut, len(b.Page),
		string(domain.BatchPending), string(domain.StatusSending))
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (r *BatchRepository) MarkDelivered(ctx context.Context, tenantID, id uuid.UUID, accepted, suppressed int) (bool, error) {
	tag, err := r.pool.Exec(ctx,
		`UPDATE campaigns.batches
		    SET status = $5, accepted = $3, suppressed = $4, page = NULL, leased_until = NULL,
		        lease_token = NULL, last_error = ''
		  WHERE tenant_id = $1 AND id = $2 AND status = $6`,
		tenantID, id, accepted, suppressed, string(domain.BatchDelivered), string(domain.BatchPending))
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (r *BatchRepository) MarkFailed(ctx context.Context, b *domain.Batch, reason string) (bool, error) {
	tag, err := r.pool.Exec(ctx,
		`UPDATE campaigns.batches
		    SET status = $5, last_error = $4, page = NULL, leased_until = NULL, lease_token = NULL
		  WHERE tenant_id = $1 AND id = $2 AND lease_token = $3 AND status = $6`,
		b.TenantID, b.ID, b.LeaseToken, domain.TruncateReason(reason),
		string(domain.BatchFailed), string(domain.BatchPending))
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (r *BatchRepository) RecordFailure(ctx context.Context, b *domain.Batch, reason string) (int, bool, error) {
	var attempts int
	err := r.pool.QueryRow(ctx,
		`UPDATE campaigns.batches
		    SET attempts = attempts + 1, last_error = $4, leased_until = NULL, lease_token = NULL
		  WHERE tenant_id = $1 AND id = $2 AND lease_token = $3 AND status = $5
		  RETURNING attempts`,
		b.TenantID, b.ID, b.LeaseToken, domain.TruncateReason(reason), string(domain.BatchPending),
	).Scan(&attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return attempts, true, nil
}

func (r *BatchRepository) Release(ctx context.Context, b *domain.Batch, lastError string) (bool, error) {
	tag, err := r.pool.Exec(ctx,
		`UPDATE campaigns.batches SET leased_until = NULL, lease_token = NULL, last_error = $4
		  WHERE tenant_id = $1 AND id = $2 AND lease_token = $3 AND status = $5`,
		b.TenantID, b.ID, b.LeaseToken, domain.TruncateReason(lastError), string(domain.BatchPending))
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (r *BatchRepository) ResetAttempts(ctx context.Context, tenantID, campaignID uuid.UUID) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE campaigns.batches SET attempts = 0
		  WHERE tenant_id = $1 AND campaign_id = $2 AND status = $3 AND attempts > 0`,
		tenantID, campaignID, string(domain.BatchPending))
	return err
}

func (r *BatchRepository) DiscardPendingPages(ctx context.Context, tenantID, campaignID uuid.UUID) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE campaigns.batches SET page = NULL, cursor_out = NULL, recipients = 0
		  WHERE tenant_id = $1 AND campaign_id = $2 AND status = $3 AND page IS NOT NULL`,
		tenantID, campaignID, string(domain.BatchPending))
	return err
}

func (r *BatchRepository) List(ctx context.Context, tenantID, campaignID uuid.UUID, page, perPage int) ([]domain.Batch, int64, error) {
	var total int64
	if err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM campaigns.batches WHERE tenant_id = $1 AND campaign_id = $2`,
		tenantID, campaignID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.pool.Query(ctx,
		`SELECT `+batchColumns+` FROM campaigns.batches
		  WHERE tenant_id = $1 AND campaign_id = $2 ORDER BY seq DESC LIMIT $3 OFFSET $4`,
		tenantID, campaignID, perPage, (page-1)*perPage)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []domain.Batch
	for rows.Next() {
		b, err := scanBatch(rows, false)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *b)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}
