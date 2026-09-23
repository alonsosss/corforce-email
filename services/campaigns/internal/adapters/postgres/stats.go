package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/campaigns/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// counterColumn traduce el tipo de evento a su columna. Es una lista cerrada: el nombre
// de la columna nunca sale de un dato de entrada.
var counterColumn = map[domain.DeliveryKind]string{
	domain.KindSent:         "sent",
	domain.KindDelivered:    "delivered",
	domain.KindBounced:      "bounced",
	domain.KindComplained:   "complained",
	domain.KindOpened:       "opened",
	domain.KindClicked:      "clicked",
	domain.KindUnsubscribed: "unsubscribed",
	domain.KindFailed:       "failed",
}

// El upsert solo toca la fila si el momento aun no constaba: RowsAffected = 1 significa
// primera apertura (o primer clic) del mensaje.
const (
	firstOpenSQL = `INSERT INTO campaigns.message_engagement (message_id, tenant_id, campaign_id, opened_at, contact_id)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (message_id) DO UPDATE
		   SET opened_at = EXCLUDED.opened_at,
		       contact_id = COALESCE(campaigns.message_engagement.contact_id, EXCLUDED.contact_id)
		WHERE campaigns.message_engagement.opened_at IS NULL`
	firstClickSQL = `INSERT INTO campaigns.message_engagement (message_id, tenant_id, campaign_id, clicked_at, contact_id)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (message_id) DO UPDATE
		   SET clicked_at = EXCLUDED.clicked_at,
		       contact_id = COALESCE(campaigns.message_engagement.contact_id, EXCLUDED.contact_id)
		WHERE campaigns.message_engagement.clicked_at IS NULL`
	// La entrega no cuenta por mensaje (el contador suma cada evento, como antes): solo
	// deja constancia de que el mensaje llego, que es lo que exige el reenvio.
	noteDeliverySQL = `INSERT INTO campaigns.message_engagement (message_id, tenant_id, campaign_id, delivered_at, contact_id)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (message_id) DO UPDATE
		   SET delivered_at = COALESCE(campaigns.message_engagement.delivered_at, EXCLUDED.delivered_at),
		       contact_id = COALESCE(campaigns.message_engagement.contact_id, EXCLUDED.contact_id)`
)

type StatsRepository struct {
	pool *db.ContextPool
}

func NewStatsRepository(pool *db.ContextPool) *StatsRepository {
	return &StatsRepository{pool: pool}
}

func (r *StatsRepository) MarkProcessed(ctx context.Context, tenantID uuid.UUID, eventID string, at time.Time) (bool, error) {
	tag, err := r.pool.Exec(ctx,
		`INSERT INTO campaigns.processed_events (event_id, tenant_id, processed_at) VALUES ($1, $2, $3)
		 ON CONFLICT (event_id) DO NOTHING`,
		eventID, tenantID, at)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (r *StatsRepository) FirstEngagement(ctx context.Context, tenantID, campaignID, messageID uuid.UUID, contactID *uuid.UUID, kind domain.DeliveryKind, at time.Time) (bool, error) {
	var sql string
	switch kind {
	case domain.KindOpened:
		sql = firstOpenSQL
	case domain.KindClicked:
		sql = firstClickSQL
	default:
		return false, fmt.Errorf("%s no se cuenta por mensaje", kind)
	}
	tag, err := r.pool.Exec(ctx, sql, messageID, tenantID, campaignID, at, contactID)
	if err != nil {
		return false, mapEngagementError(err)
	}
	return tag.RowsAffected() == 1, nil
}

func (r *StatsRepository) NoteDelivery(ctx context.Context, tenantID, campaignID, messageID uuid.UUID, contactID *uuid.UUID, at time.Time) error {
	_, err := r.pool.Exec(ctx, noteDeliverySQL, messageID, tenantID, campaignID, at, contactID)
	return mapEngagementError(err)
}

func mapEngagementError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolation {
		return domain.ErrCampaignNotFound
	}
	return err
}

func (r *StatsRepository) IncrementCounter(ctx context.Context, tenantID, campaignID uuid.UUID, kind domain.DeliveryKind) (bool, error) {
	col, ok := counterColumn[kind]
	if !ok {
		return false, fmt.Errorf("tipo de evento sin contador: %s", kind)
	}
	tag, err := r.pool.Exec(ctx,
		`UPDATE campaigns.campaigns SET `+col+` = `+col+` + 1 WHERE tenant_id = $1 AND id = $2`,
		tenantID, campaignID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (r *StatsRepository) PruneProcessed(ctx context.Context, tenantID uuid.UUID, before time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM campaigns.processed_events WHERE tenant_id = $1 AND processed_at < $2`, tenantID, before)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
