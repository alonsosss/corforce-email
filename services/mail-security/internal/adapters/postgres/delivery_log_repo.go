package postgres

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/google/uuid"
)

// DeliveryLogRepository implementa ports.DeliveryLogRepository sobre mail_security.delivery_events.
type DeliveryLogRepository struct {
	pool *db.ContextPool
}

func NewDeliveryLogRepository(pool *db.ContextPool) *DeliveryLogRepository {
	return &DeliveryLogRepository{pool: pool}
}

const deliveryColumns = `id, tenant_id, direction, queue_id, message_id, sender, recipient, status, dsn, relay, reason,
	COALESCE(delay_seconds::text, ''), sasl_username, client_ip, occurred_at, created_at`

func (r *DeliveryLogRepository) Insert(ctx context.Context, e *domain.DeliveryEvent) (bool, error) {
	var delay *string
	if e.DelaySeconds != "" {
		delay = &e.DelaySeconds
	}
	tag, err := r.pool.Exec(ctx, `
		INSERT INTO mail_security.delivery_events
		    (id, tenant_id, direction, queue_id, message_id, sender, recipient, status, dsn, relay, reason,
		     delay_seconds, sasl_username, client_ip, occurred_at, event_key)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12::numeric, $13, $14, $15, $16)
		ON CONFLICT (tenant_id, event_key) DO NOTHING`,
		e.ID, e.TenantID, e.Direction, e.QueueID, e.MessageID, e.Sender, e.Recipient, e.Status, e.DSN, e.Relay,
		e.Reason, delay, e.SASLUsername, e.ClientIP, e.OccurredAt, e.EventKey)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (r *DeliveryLogRepository) List(ctx context.Context, tenantID uuid.UUID, f domain.DeliveryFilter) ([]domain.DeliveryEvent, int64, error) {
	where := []string{"tenant_id = $1"}
	args := []any{tenantID}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "?", "$"+strconv.Itoa(len(args))))
	}
	if f.Direction != "" {
		add("direction = ?", f.Direction)
	}
	if f.Status != "" {
		add("status = ?", f.Status)
	}
	if f.Address != "" {
		add("(sender = ? OR recipient = ?)", f.Address)
	}
	if f.DateFrom != nil {
		add("occurred_at >= ?", *f.DateFrom)
	}
	if f.DateTo != nil {
		add("occurred_at < ?", *f.DateTo)
	}
	filter := strings.Join(where, " AND ")

	var total int64
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM mail_security.delivery_events WHERE `+filter, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, f.PerPage, (f.Page-1)*f.PerPage)
	rows, err := r.pool.Query(ctx, `SELECT `+deliveryColumns+` FROM mail_security.delivery_events WHERE `+filter+
		` ORDER BY occurred_at DESC, id LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []domain.DeliveryEvent{}
	for rows.Next() {
		var e domain.DeliveryEvent
		if err := rows.Scan(&e.ID, &e.TenantID, &e.Direction, &e.QueueID, &e.MessageID, &e.Sender, &e.Recipient, &e.Status,
			&e.DSN, &e.Relay, &e.Reason, &e.DelaySeconds, &e.SASLUsername, &e.ClientIP, &e.OccurredAt, &e.CreatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	return out, total, rows.Err()
}

// PruneBefore borra por tandas para no retener un bloqueo largo sobre la tabla.
func (r *DeliveryLogRepository) PruneBefore(ctx context.Context, before time.Time, limit int) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
		DELETE FROM mail_security.delivery_events WHERE id IN (
			SELECT id FROM mail_security.delivery_events WHERE occurred_at < $1 LIMIT $2)`, before, limit)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
