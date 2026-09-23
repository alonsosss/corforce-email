package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/analytics/internal/domain"
	"github.com/google/uuid"
)

// Links implementa ports.LinkRepository sobre campaign_link_stats y campaign_link_clicks.
type Links struct{ db *db.ContextPool }

func NewLinks(pool *db.ContextPool) *Links { return &Links{db: pool} }

// RecordClick decide primero en que fila cae el clic: la de su URL si ya existe o si la
// campana aun no llego al tope de URL distintas; si no, la de OtherLinks. El tope se mide
// sin bloquear la campana: dos URL nuevas a la vez pueden pasarlo por una fila, a cambio
// de no serializar los clics de toda la campana.
func (l *Links) RecordClick(ctx context.Context, c domain.LinkClick) error {
	link := c.URL
	hash := domain.LinkHash(link)
	var known bool
	if err := l.db.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM analytics.campaign_link_stats
		                 WHERE tenant_id = $1 AND campaign_id = $2 AND url_hash = $3)`,
		c.TenantID, c.CampaignID, hash).Scan(&known); err != nil {
		return fmt.Errorf("buscar enlace: %w", err)
	}
	if !known {
		var distinct int
		if err := l.db.QueryRow(ctx,
			`SELECT count(*) FROM analytics.campaign_link_stats
			  WHERE tenant_id = $1 AND campaign_id = $2 AND url <> ''`,
			c.TenantID, c.CampaignID).Scan(&distinct); err != nil {
			return fmt.Errorf("contar enlaces: %w", err)
		}
		if distinct >= domain.MaxLinksPerCampaign {
			link = domain.OtherLinks
			hash = domain.LinkHash(link)
		}
	}
	tag, err := l.db.Exec(ctx,
		`INSERT INTO analytics.campaign_link_clicks (message_id, url_hash, tenant_id, campaign_id, clicked_at)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (message_id, url_hash) DO NOTHING`,
		c.MessageID, hash, c.TenantID, c.CampaignID, c.At)
	if err != nil {
		return fmt.Errorf("registrar clic del mensaje: %w", err)
	}
	unique := tag.RowsAffected()
	if _, err := l.db.Exec(ctx,
		`INSERT INTO analytics.campaign_link_stats
		        (tenant_id, campaign_id, url_hash, url, clicks, unique_clicks, first_clicked_at, last_clicked_at)
		 VALUES ($1, $2, $3, $4, 1, $5, $6, $6)
		 ON CONFLICT (tenant_id, campaign_id, url_hash) DO UPDATE
		    SET clicks = campaign_link_stats.clicks + 1,
		        unique_clicks = campaign_link_stats.unique_clicks + EXCLUDED.unique_clicks,
		        first_clicked_at = LEAST(campaign_link_stats.first_clicked_at, EXCLUDED.first_clicked_at),
		        last_clicked_at = GREATEST(campaign_link_stats.last_clicked_at, EXCLUDED.last_clicked_at)`,
		c.TenantID, c.CampaignID, hash, link, unique, c.At); err != nil {
		return fmt.Errorf("sumar clic del enlace: %w", err)
	}
	return nil
}

func (l *Links) PruneClicks(ctx context.Context, tenantID uuid.UUID, before time.Time) (int64, error) {
	return pruneLoop(ctx, l.db,
		`DELETE FROM analytics.campaign_link_clicks WHERE (message_id, url_hash) IN (
		   SELECT message_id, url_hash FROM analytics.campaign_link_clicks
		    WHERE tenant_id = $1 AND clicked_at < $2 LIMIT $3)`, tenantID, before)
}

// CampaignLinks lee los enlaces de una campana con mas clics y los totales de todas.
func (r *Reports) CampaignLinks(ctx context.Context, tenantID, campaignID uuid.UUID, limit int) (*domain.CampaignLinks, error) {
	out := &domain.CampaignLinks{CampaignID: campaignID, Links: []domain.LinkStats{}}
	if err := r.db.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE url <> ''), COALESCE(sum(clicks), 0)::bigint
		   FROM analytics.campaign_link_stats
		  WHERE tenant_id = $1 AND campaign_id = $2`, tenantID, campaignID,
	).Scan(&out.TotalLinks, &out.TotalClicks); err != nil {
		return nil, fmt.Errorf("totales de enlaces: %w", err)
	}
	rows, err := r.db.Query(ctx,
		`SELECT url, clicks, unique_clicks, first_clicked_at, last_clicked_at
		   FROM analytics.campaign_link_stats
		  WHERE tenant_id = $1 AND campaign_id = $2
		  ORDER BY clicks DESC, url
		  LIMIT $3`, tenantID, campaignID, limit)
	if err != nil {
		return nil, fmt.Errorf("enlaces de la campana: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var s domain.LinkStats
		if err := rows.Scan(&s.URL, &s.Clicks, &s.UniqueClicks, &s.FirstClickedAt, &s.LastClickedAt); err != nil {
			return nil, fmt.Errorf("leer enlace: %w", err)
		}
		out.Links = append(out.Links, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("enlaces de la campana: %w", err)
	}
	return out, nil
}
