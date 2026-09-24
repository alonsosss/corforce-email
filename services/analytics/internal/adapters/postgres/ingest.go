package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/analytics/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// pruneBatch acota cada DELETE de la poda: lotes cortos no retienen bloqueos largos ni
// generan transacciones enormes en empresas con mucho volumen.
const pruneBatch = 5000

// errAggregateMissing es una inconsistencia: se pidio descontar de una fila de agregado
// que no existe. No es un fallo del evento sino de los datos; el consumidor lo reintenta
// y, agotado, el evento queda en la DLQ para inspeccion.
var errAggregateMissing = errors.New("analytics: descuento sobre un agregado inexistente")

// Ledger implementa ports.EventLedger sobre analytics.processed_events.
type Ledger struct{ db *db.ContextPool }

func NewLedger(pool *db.ContextPool) *Ledger { return &Ledger{db: pool} }

func (l *Ledger) MarkProcessed(ctx context.Context, tenantID, eventID uuid.UUID) (bool, error) {
	tag, err := l.db.Exec(ctx,
		`INSERT INTO analytics.processed_events (event_id, tenant_id) VALUES ($1, $2)
		 ON CONFLICT (event_id) DO NOTHING`, eventID, tenantID)
	if err != nil {
		return false, fmt.Errorf("registrar evento procesado: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (l *Ledger) PruneProcessed(ctx context.Context, tenantID uuid.UUID, before time.Time) (int64, error) {
	return pruneLoop(ctx, l.db,
		`DELETE FROM analytics.processed_events WHERE event_id IN (
		   SELECT event_id FROM analytics.processed_events
		    WHERE tenant_id = $1 AND processed_at < $2 LIMIT $3)`, tenantID, before)
}

// Facts implementa ports.FactRepository sobre analytics.message_facts.
type Facts struct{ db *db.ContextPool }

func NewFacts(pool *db.ContextPool) *Facts { return &Facts{db: pool} }

func (f *Facts) LockOrCreate(ctx context.Context, seed domain.MessageFact) (*domain.MessageFact, error) {
	if _, err := f.db.Exec(ctx,
		`INSERT INTO analytics.message_facts (message_id, tenant_id, class, campaign_id, recipient_domain)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (message_id) DO NOTHING`,
		seed.MessageID, seed.TenantID, string(seed.Class), seed.CampaignID, nullable(seed.RecipientDomain)); err != nil {
		return nil, fmt.Errorf("crear fila del mensaje: %w", err)
	}
	var out domain.MessageFact
	var class string
	var recipientDomain, bounceType *string
	err := f.db.QueryRow(ctx,
		`SELECT message_id, tenant_id, class, campaign_id, recipient_domain, sent_at, delivered_at,
		        first_opened_at, first_clicked_at, bounced_at, bounce_type, complained_at,
		        unsubscribed_at, failed_at
		   FROM analytics.message_facts
		  WHERE message_id = $1 AND tenant_id = $2
		    FOR UPDATE`, seed.MessageID, seed.TenantID,
	).Scan(&out.MessageID, &out.TenantID, &class, &out.CampaignID, &recipientDomain, &out.SentAt,
		&out.DeliveredAt, &out.FirstOpenedAt, &out.FirstClickedAt, &out.BouncedAt, &bounceType,
		&out.ComplainedAt, &out.UnsubscribedAt, &out.FailedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: el mensaje %s pertenece a otra empresa", domain.ErrInvalidEvent, seed.MessageID)
	}
	if err != nil {
		return nil, fmt.Errorf("bloquear fila del mensaje: %w", err)
	}
	out.Class = domain.Class(class)
	if recipientDomain != nil {
		out.RecipientDomain = *recipientDomain
	}
	if bounceType != nil {
		out.BounceKind = domain.BounceKind(*bounceType)
	}
	return &out, nil
}

func (f *Facts) Save(ctx context.Context, m *domain.MessageFact) error {
	tag, err := f.db.Exec(ctx,
		`UPDATE analytics.message_facts
		    SET sent_at = $3, delivered_at = $4, first_opened_at = $5, first_clicked_at = $6,
		        bounced_at = $7, bounce_type = $8, complained_at = $9, unsubscribed_at = $10,
		        failed_at = $11
		  WHERE message_id = $1 AND tenant_id = $2`,
		m.MessageID, m.TenantID, m.SentAt, m.DeliveredAt, m.FirstOpenedAt, m.FirstClickedAt,
		m.BouncedAt, nullable(string(m.BounceKind)), m.ComplainedAt, m.UnsubscribedAt, m.FailedAt)
	if err != nil {
		return fmt.Errorf("guardar fila del mensaje: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("guardar fila del mensaje %s: no existe", m.MessageID)
	}
	return nil
}

func (f *Facts) PruneInactive(ctx context.Context, tenantID uuid.UUID, before time.Time) (int64, error) {
	return pruneLoop(ctx, f.db,
		`DELETE FROM analytics.message_facts WHERE message_id IN (
		   SELECT message_id FROM analytics.message_facts
		    WHERE tenant_id = $1 AND updated_at < $2 LIMIT $3)`, tenantID, before)
}

// Stats implementa ports.StatsRepository sobre los tres agregados diarios.
type Stats struct{ db *db.ContextPool }

func NewStats(pool *db.ContextPool) *Stats { return &Stats{db: pool} }

// Apply escribe siempre en el mismo orden (clase, campana, dominio): junto con el orden de
// dias de domain.GroupByDay, es el orden de bloqueo de las filas.
func (s *Stats) Apply(ctx context.Context, tenantID uuid.UUID, dims domain.Dimensions, day domain.DayCounters) error {
	type target struct {
		table aggregate
		keys  []any
	}
	targets := []target{{classAggregate, []any{tenantID, day.Day, string(dims.Class)}}}
	if dims.CampaignID != nil {
		targets = append(targets, target{campaignAggregate, []any{tenantID, day.Day, *dims.CampaignID}})
	}
	if dims.RecipientDomain != "" {
		targets = append(targets, target{domainAggregate, []any{tenantID, day.Day, string(dims.Class), dims.RecipientDomain}})
	}
	values := counterArgs(day.Counters)
	additive := day.Counters.NonNegative()
	for _, t := range targets {
		args := append(append([]any{}, t.keys...), values...)
		if additive {
			if _, err := s.db.Exec(ctx, t.table.upsert, args...); err != nil {
				return fmt.Errorf("sumar en %s: %w", t.table.name, err)
			}
			continue
		}
		tag, err := s.db.Exec(ctx, t.table.increment, args...)
		if err != nil {
			return fmt.Errorf("corregir %s: %w", t.table.name, err)
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("%w: %s del %s", errAggregateMissing, t.table.name, domain.FormatDate(day.Day))
		}
	}
	return nil
}

// Campaigns implementa ports.CampaignRepository sobre analytics.campaigns_seen.
type Campaigns struct{ db *db.ContextPool }

func NewCampaigns(pool *db.ContextPool) *Campaigns { return &Campaigns{db: pool} }

func (c *Campaigns) CreateIfAbsent(ctx context.Context, s domain.CampaignSeen) (bool, error) {
	tag, err := c.db.Exec(ctx,
		`INSERT INTO analytics.campaigns_seen (campaign_id, tenant_id, status, status_at, started_at, completed_at)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (campaign_id) DO NOTHING`,
		s.CampaignID, s.TenantID, s.Status, s.StatusAt, s.StartedAt, s.CompletedAt)
	if err != nil {
		return false, fmt.Errorf("registrar campana: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (c *Campaigns) Lock(ctx context.Context, tenantID, campaignID uuid.UUID) (*domain.CampaignSeen, error) {
	var s domain.CampaignSeen
	err := c.db.QueryRow(ctx,
		`SELECT campaign_id, tenant_id, status, status_at, started_at, completed_at
		   FROM analytics.campaigns_seen
		  WHERE campaign_id = $1 AND tenant_id = $2
		    FOR UPDATE`, campaignID, tenantID,
	).Scan(&s.CampaignID, &s.TenantID, &s.Status, &s.StatusAt, &s.StartedAt, &s.CompletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: la campaña %s pertenece a otra empresa", domain.ErrInvalidEvent, campaignID)
	}
	if err != nil {
		return nil, fmt.Errorf("bloquear campana: %w", err)
	}
	return &s, nil
}

func (c *Campaigns) Save(ctx context.Context, s *domain.CampaignSeen) error {
	tag, err := c.db.Exec(ctx,
		`UPDATE analytics.campaigns_seen
		    SET status = $3, status_at = $4, started_at = $5, completed_at = $6
		  WHERE campaign_id = $1 AND tenant_id = $2`,
		s.CampaignID, s.TenantID, s.Status, s.StatusAt, s.StartedAt, s.CompletedAt)
	if err != nil {
		return fmt.Errorf("guardar campana: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("guardar campaña %s: no existe", s.CampaignID)
	}
	return nil
}

// pruneLoop repite un DELETE por lotes ($1 empresa, $2 limite temporal, $3 lote) hasta
// que un lote sale incompleto.
func pruneLoop(ctx context.Context, q *db.ContextPool, sql string, tenantID uuid.UUID, before time.Time) (int64, error) {
	var total int64
	for {
		tag, err := q.Exec(ctx, sql, tenantID, before, pruneBatch)
		if err != nil {
			return total, fmt.Errorf("podar: %w", err)
		}
		total += tag.RowsAffected()
		if tag.RowsAffected() < pruneBatch {
			return total, nil
		}
	}
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
