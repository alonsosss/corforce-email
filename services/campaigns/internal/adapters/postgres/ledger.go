package postgres

import (
	"context"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/campaigns/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// RecipientLedger lleva campaigns.recipients (quien recibio cada ronda) y la fase y
// variante de cada mensaje en campaigns.message_engagement.
type RecipientLedger struct {
	pool *db.ContextPool
}

func NewRecipientLedger(pool *db.ContextPool) *RecipientLedger {
	return &RecipientLedger{pool: pool}
}

func (r *RecipientLedger) RecordRecipients(ctx context.Context, tenantID, campaignID, phaseID uuid.UUID, round domain.Round, contactIDs []uuid.UUID) error {
	if len(contactIDs) == 0 {
		return nil
	}
	_, err := r.pool.Exec(ctx,
		`INSERT INTO campaigns.recipients (campaign_id, round, contact_id, tenant_id, phase_id)
		 SELECT $2, $3, id, $1, $4 FROM unnest($5::uuid[]) AS id
		 ON CONFLICT (campaign_id, round, contact_id) DO NOTHING`,
		tenantID, campaignID, string(round), phaseID, contactIDs)
	return err
}

// RecordMessages admite que el evento de entrega o de apertura del mensaje llegara antes
// que el cierre del lote: si la fila ya existe, solo le pone fase y variante.
func (r *RecipientLedger) RecordMessages(ctx context.Context, tenantID, campaignID uuid.UUID, kind domain.PhaseKind, variant *int, messageIDs []uuid.UUID) error {
	if len(messageIDs) == 0 {
		return nil
	}
	_, err := r.pool.Exec(ctx,
		`INSERT INTO campaigns.message_engagement (message_id, tenant_id, campaign_id, phase_kind, variant)
		 SELECT id, $1, $2, $3, $4 FROM unnest($5::uuid[]) AS id
		 ON CONFLICT (message_id) DO UPDATE SET phase_kind = EXCLUDED.phase_kind, variant = EXCLUDED.variant`,
		tenantID, campaignID, string(kind), variant, messageIDs)
	return err
}

func (r *RecipientLedger) Sent(ctx context.Context, tenantID, campaignID uuid.UUID, contactIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	return r.contactSet(ctx,
		`SELECT contact_id FROM campaigns.recipients
		  WHERE tenant_id = $1 AND campaign_id = $2 AND round = 'initial' AND contact_id = ANY($3::uuid[])`,
		tenantID, campaignID, contactIDs)
}

// ResendEligible: recibio la ronda inicial, algun mensaje suyo de esa ronda consta como
// entregado, ninguno consta abierto ni con clic y aun no recibio el reenvio. Una apertura
// que el cliente de correo no notifica (imagenes bloqueadas) cuenta como no abierto; una
// que notifica el proxy de privacidad de Apple cuenta como abierta aunque nadie leyera.
func (r *RecipientLedger) ResendEligible(ctx context.Context, tenantID, campaignID uuid.UUID, contactIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	return r.contactSet(ctx,
		`SELECT rc.contact_id FROM campaigns.recipients rc
		  WHERE rc.tenant_id = $1 AND rc.campaign_id = $2 AND rc.round = 'initial' AND rc.contact_id = ANY($3::uuid[])
		    AND EXISTS (SELECT 1 FROM campaigns.message_engagement m
		                 WHERE m.campaign_id = rc.campaign_id AND m.contact_id = rc.contact_id
		                   AND m.delivered_at IS NOT NULL AND m.phase_kind IS DISTINCT FROM 'resend')
		    AND NOT EXISTS (SELECT 1 FROM campaigns.message_engagement m
		                     WHERE m.campaign_id = rc.campaign_id AND m.contact_id = rc.contact_id
		                       AND (m.opened_at IS NOT NULL OR m.clicked_at IS NOT NULL))
		    AND NOT EXISTS (SELECT 1 FROM campaigns.recipients x
		                     WHERE x.campaign_id = rc.campaign_id AND x.round = 'resend' AND x.contact_id = rc.contact_id)`,
		tenantID, campaignID, contactIDs)
}

func (r *RecipientLedger) contactSet(ctx context.Context, sql string, tenantID, campaignID uuid.UUID, contactIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	out := map[uuid.UUID]bool{}
	if len(contactIDs) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, sql, tenantID, campaignID, contactIDs)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

func (r *RecipientLedger) Engagement(ctx context.Context, tenantID, campaignID uuid.UUID) ([]domain.PhaseEngagement, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT phase_kind, variant, count(*), count(delivered_at), count(opened_at), count(clicked_at)
		   FROM campaigns.message_engagement
		  WHERE tenant_id = $1 AND campaign_id = $2 AND phase_kind IS NOT NULL
		  GROUP BY phase_kind, variant
		  ORDER BY phase_kind, variant NULLS FIRST`,
		tenantID, campaignID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.PhaseEngagement
	for rows.Next() {
		var (
			e    domain.PhaseEngagement
			kind string
		)
		if err := rows.Scan(&kind, &e.Variant, &e.Accepted, &e.Delivered, &e.Opened, &e.Clicked); err != nil {
			return nil, err
		}
		e.Kind = domain.PhaseKind(kind)
		out = append(out, e)
	}
	return out, rows.Err()
}
