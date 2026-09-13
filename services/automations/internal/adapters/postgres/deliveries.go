package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/automations/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const deliveryColumns = `id, tenant_id, event_id, contact_id, status, reason, message_id, attempts, template_id,
	from_email, from_name, reply_to, sent_at, created_at, updated_at`

// inFlightOrSent es el predicado del indice parcial idx_automations_doi_deliveries_contact.
// Va como literal: el planificador solo usa el indice si ve el mismo literal.
const inFlightOrSent = `status IN ('` + string(domain.DOIPending) + `', '` + string(domain.DOISent) + `')`

type DeliveryRepository struct {
	pool *db.ContextPool
}

func NewDeliveryRepository(pool *db.ContextPool) *DeliveryRepository {
	return &DeliveryRepository{pool: pool}
}

func scanDelivery(row pgx.Row) (*domain.DOIDelivery, error) {
	var (
		d      domain.DOIDelivery
		status string
	)
	if err := row.Scan(&d.ID, &d.TenantID, &d.EventID, &d.ContactID, &status, &d.Reason, &d.MessageID, &d.Attempts,
		&d.TemplateID, &d.FromEmail, &d.FromName, &d.ReplyTo, &d.SentAt, &d.CreatedAt, &d.UpdatedAt); err != nil {
		return nil, err
	}
	d.Status = domain.DOIStatus(status)
	return &d, nil
}

// LockContact toma un advisory lock de transaccion con la empresa y el contacto. Solo
// tiene efecto dentro de una transaccion: se suelta sola al confirmar o deshacer.
func (r *DeliveryRepository) LockContact(ctx context.Context, tenantID, contactID uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		"automations:doi:"+tenantID.String()+":"+contactID.String())
	return err
}

func (r *DeliveryRepository) GetByEvent(ctx context.Context, tenantID uuid.UUID, eventID string) (*domain.DOIDelivery, error) {
	d, err := scanDelivery(r.pool.QueryRow(ctx,
		`SELECT `+deliveryColumns+` FROM automations.doi_deliveries WHERE tenant_id = $1 AND event_id = $2`,
		tenantID, eventID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return d, err
}

func (r *DeliveryRepository) CountRecent(ctx context.Context, tenantID, contactID uuid.UUID, dayStart, monthStart time.Time, excludeEventID string) (int, int, error) {
	var day, month int
	err := r.pool.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE created_at >= $3), count(*)
		   FROM automations.doi_deliveries
		  WHERE tenant_id = $1 AND contact_id = $2 AND `+inFlightOrSent+`
		    AND created_at >= $4 AND event_id <> $5`,
		tenantID, contactID, dayStart, monthStart, excludeEventID,
	).Scan(&day, &month)
	return day, month, err
}

func (r *DeliveryRepository) Insert(ctx context.Context, d *domain.DOIDelivery) error {
	return r.pool.QueryRow(ctx,
		`INSERT INTO automations.doi_deliveries (id, tenant_id, event_id, contact_id, status, reason, template_id,
		        from_email, from_name, reply_to)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 RETURNING created_at, updated_at`,
		d.ID, d.TenantID, d.EventID, d.ContactID, string(d.Status), d.Reason, d.TemplateID,
		d.FromEmail, d.FromName, d.ReplyTo,
	).Scan(&d.CreatedAt, &d.UpdatedAt)
}

func (r *DeliveryRepository) MarkSent(ctx context.Context, tenantID, id uuid.UUID, messageID *uuid.UUID, at time.Time) (bool, error) {
	tag, err := r.pool.Exec(ctx,
		`UPDATE automations.doi_deliveries SET status = $3, message_id = $4, sent_at = $5, reason = ''
		  WHERE tenant_id = $1 AND id = $2 AND status = $6`,
		tenantID, id, string(domain.DOISent), messageID, at, string(domain.DOIPending))
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (r *DeliveryRepository) MarkFailed(ctx context.Context, tenantID, id uuid.UUID, reason string) (bool, error) {
	tag, err := r.pool.Exec(ctx,
		`UPDATE automations.doi_deliveries SET status = $3, reason = $4
		  WHERE tenant_id = $1 AND id = $2 AND status = $5`,
		tenantID, id, string(domain.DOIFailed), domain.TruncateReason(reason), string(domain.DOIPending))
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (r *DeliveryRepository) RecordAttempt(ctx context.Context, tenantID, id uuid.UUID, reason string) (int, error) {
	var attempts int
	err := r.pool.QueryRow(ctx,
		`UPDATE automations.doi_deliveries SET attempts = attempts + 1, reason = $3
		  WHERE tenant_id = $1 AND id = $2 AND status = $4
		  RETURNING attempts`,
		tenantID, id, domain.TruncateReason(reason), string(domain.DOIPending),
	).Scan(&attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return attempts, err
}

func (r *DeliveryRepository) List(ctx context.Context, tenantID uuid.UUID, status domain.DOIStatus, page, perPage int) ([]domain.DOIDelivery, int64, error) {
	var total int64
	if err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM automations.doi_deliveries WHERE tenant_id = $1 AND ($2 = '' OR status = $2)`,
		tenantID, string(status)).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.pool.Query(ctx,
		`SELECT `+deliveryColumns+` FROM automations.doi_deliveries
		  WHERE tenant_id = $1 AND ($2 = '' OR status = $2)
		  ORDER BY created_at DESC, id LIMIT $3 OFFSET $4`,
		tenantID, string(status), perPage, offset(page, perPage))
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []domain.DOIDelivery{}
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *d)
	}
	return out, total, rows.Err()
}
