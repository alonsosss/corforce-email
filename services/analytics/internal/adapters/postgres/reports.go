package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/analytics/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Reports implementa ports.ReportRepository. Un filtro de clase vacio ($4) no filtra.
type Reports struct{ db *db.ContextPool }

func NewReports(pool *db.ContextPool) *Reports { return &Reports{db: pool} }

var (
	totalsSQL = `SELECT ` + sumColumns("") + `
	   FROM analytics.daily_class_stats
	  WHERE tenant_id = $1 AND day BETWEEN $2 AND $3 AND ($4::text = '' OR class = $4)`

	// La serie sale de generate_series: un punto por dia del rango, en cero si no hubo
	// actividad, sin que el panel tenga que rellenar huecos.
	seriesSQL = `SELECT g.day::date, ` + sumColumns("s.") + `
	   FROM generate_series($2::date, $3::date, interval '1 day') AS g(day)
	   LEFT JOIN analytics.daily_class_stats s
	          ON s.tenant_id = $1 AND s.day = g.day::date AND ($4::text = '' OR s.class = $4)
	  GROUP BY g.day
	  ORDER BY g.day`

	campaignIDsSQL = `SELECT campaign_id FROM analytics.campaigns_seen WHERE tenant_id = $1
	  UNION
	 SELECT campaign_id FROM analytics.daily_campaign_stats WHERE tenant_id = $1`

	countCampaignsSQL = `SELECT count(*) FROM (` + campaignIDsSQL + `) ids`

	listCampaignsSQL = `WITH ids AS (` + campaignIDsSQL + `),
	 page AS (
	   SELECT i.campaign_id, s.status, s.started_at, s.completed_at
	     FROM ids i
	     LEFT JOIN analytics.campaigns_seen s ON s.tenant_id = $1 AND s.campaign_id = i.campaign_id
	    ORDER BY s.started_at DESC NULLS LAST, i.campaign_id
	    LIMIT $2 OFFSET $3
	 )
	 SELECT p.campaign_id, p.status, p.started_at, p.completed_at, min(d.day), max(d.day), ` + sumColumns("d.") + `
	   FROM page p
	   LEFT JOIN analytics.daily_campaign_stats d ON d.tenant_id = $1 AND d.campaign_id = p.campaign_id
	  GROUP BY p.campaign_id, p.status, p.started_at, p.completed_at
	  ORDER BY p.started_at DESC NULLS LAST, p.campaign_id`

	getCampaignSQL = `SELECT s.status, s.started_at, s.completed_at, min(d.day), max(d.day), ` + sumColumns("d.") + `
	   FROM (SELECT $2::uuid AS campaign_id) c
	   LEFT JOIN analytics.campaigns_seen s ON s.tenant_id = $1 AND s.campaign_id = c.campaign_id
	   LEFT JOIN analytics.daily_campaign_stats d ON d.tenant_id = $1 AND d.campaign_id = c.campaign_id
	  GROUP BY s.status, s.started_at, s.completed_at`

	campaignSeriesSQL = `SELECT g.day::date, ` + coalesceColumns("d.") + `
	   FROM generate_series($3::date, $4::date, interval '1 day') AS g(day)
	   LEFT JOIN analytics.daily_campaign_stats d
	          ON d.tenant_id = $1 AND d.campaign_id = $2 AND d.day = g.day::date
	  ORDER BY g.day`

	topDomainsSQL = `SELECT recipient_domain, ` + sumColumns("") + `
	   FROM analytics.daily_domain_stats
	  WHERE tenant_id = $1 AND day BETWEEN $2 AND $3 AND ($4::text = '' OR class = $4)
	  GROUP BY recipient_domain
	  ORDER BY sum(sent) DESC, recipient_domain
	  LIMIT $5`
)

func (r *Reports) Totals(ctx context.Context, tenantID uuid.UUID, q domain.ClassQuery) (domain.Counters, error) {
	var c domain.Counters
	if err := r.db.QueryRow(ctx, totalsSQL, tenantID, q.Range.From, q.Range.To, string(q.Class)).Scan(counterDest(&c)...); err != nil {
		return domain.Counters{}, fmt.Errorf("totales: %w", err)
	}
	return c, nil
}

func (r *Reports) Series(ctx context.Context, tenantID uuid.UUID, q domain.ClassQuery) ([]domain.DayCounters, error) {
	rows, err := r.db.Query(ctx, seriesSQL, tenantID, q.Range.From, q.Range.To, string(q.Class))
	if err != nil {
		return nil, fmt.Errorf("serie diaria: %w", err)
	}
	return scanSeries(rows, q.Range.Days())
}

func (r *Reports) CountCampaigns(ctx context.Context, tenantID uuid.UUID) (int64, error) {
	var n int64
	if err := r.db.QueryRow(ctx, countCampaignsSQL, tenantID).Scan(&n); err != nil {
		return 0, fmt.Errorf("contar campanas: %w", err)
	}
	return n, nil
}

func (r *Reports) ListCampaigns(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]domain.CampaignSummary, error) {
	rows, err := r.db.Query(ctx, listCampaignsSQL, tenantID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("listar campanas: %w", err)
	}
	defer rows.Close()
	out := make([]domain.CampaignSummary, 0, limit)
	for rows.Next() {
		var s domain.CampaignSummary
		dest := append([]any{&s.CampaignID, &s.Status, &s.StartedAt, &s.CompletedAt, &s.FirstDay, &s.LastDay}, counterDest(&s.Totals)...)
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("leer campana: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listar campanas: %w", err)
	}
	return out, nil
}

func (r *Reports) GetCampaign(ctx context.Context, tenantID, campaignID uuid.UUID) (*domain.CampaignSummary, error) {
	s := domain.CampaignSummary{CampaignID: campaignID}
	dest := append([]any{&s.Status, &s.StartedAt, &s.CompletedAt, &s.FirstDay, &s.LastDay}, counterDest(&s.Totals)...)
	if err := r.db.QueryRow(ctx, getCampaignSQL, tenantID, campaignID).Scan(dest...); err != nil {
		return nil, fmt.Errorf("leer campana: %w", err)
	}
	if s.Status == nil && s.FirstDay == nil {
		return nil, domain.ErrCampaignNotFound
	}
	return &s, nil
}

func (r *Reports) CampaignSeries(ctx context.Context, tenantID, campaignID uuid.UUID, rg domain.Range) ([]domain.DayCounters, error) {
	rows, err := r.db.Query(ctx, campaignSeriesSQL, tenantID, campaignID, rg.From, rg.To)
	if err != nil {
		return nil, fmt.Errorf("serie de campana: %w", err)
	}
	return scanSeries(rows, rg.Days())
}

func (r *Reports) TopDomains(ctx context.Context, tenantID uuid.UUID, q domain.ClassQuery, limit int) ([]domain.DomainStats, error) {
	rows, err := r.db.Query(ctx, topDomainsSQL, tenantID, q.Range.From, q.Range.To, string(q.Class), limit)
	if err != nil {
		return nil, fmt.Errorf("dominios destino: %w", err)
	}
	defer rows.Close()
	out := make([]domain.DomainStats, 0, limit)
	for rows.Next() {
		var d domain.DomainStats
		if err := rows.Scan(append([]any{&d.RecipientDomain}, counterDest(&d.Totals)...)...); err != nil {
			return nil, fmt.Errorf("leer dominio destino: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("dominios destino: %w", err)
	}
	return out, nil
}

func scanSeries(rows pgx.Rows, days int) ([]domain.DayCounters, error) {
	defer rows.Close()
	out := make([]domain.DayCounters, 0, days)
	for rows.Next() {
		var p domain.DayCounters
		var day time.Time
		if err := rows.Scan(append([]any{&day}, counterDest(&p.Counters)...)...); err != nil {
			return nil, fmt.Errorf("leer dia de la serie: %w", err)
		}
		p.Day = domain.Day(day)
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("serie diaria: %w", err)
	}
	return out, nil
}
