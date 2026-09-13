// Package postgres implementa los repositorios del servicio sobre la base de la empresa
// (esquema campaigns). El pool o la transaccion salen del contexto (db.ContextPool).
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/campaigns/internal/domain"
	"github.com/alonsosss/corforce-email/services/campaigns/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	uniqueViolation     = "23505"
	foreignKeyViolation = "23503"
	nameIndex           = "uq_campaigns_tenant_name"
)

var campaignFields = []string{
	"id", "tenant_id", "name", "description", "status", "pause_reason", "failure_reason",
	"template_id", "template_version", "from_email", "from_name", "reply_to", "audience",
	"scheduled_at", "started_at", "completed_at", "resume_after",
	"targeted", "accepted", "suppressed", "sent", "delivered", "bounced", "complained",
	"opened", "clicked", "unsubscribed", "failed",
	"created_by", "created_at", "updated_at",
}

var campaignColumns = strings.Join(campaignFields, ", ")

// qualified antepone el alias a cada columna: en un UPDATE ... FROM con CTE, "id" a
// secas es ambiguo.
func qualified(alias string) string {
	out := make([]string, len(campaignFields))
	for i, f := range campaignFields {
		out[i] = alias + "." + f
	}
	return strings.Join(out, ", ")
}

type CampaignRepository struct {
	pool *db.ContextPool
}

func NewCampaignRepository(pool *db.ContextPool) *CampaignRepository {
	return &CampaignRepository{pool: pool}
}

func scanCampaign(row pgx.Row) (*domain.Campaign, error) {
	var (
		c        domain.Campaign
		status   string
		audience []byte
	)
	err := row.Scan(&c.ID, &c.TenantID, &c.Name, &c.Description, &status, &c.PauseReason, &c.FailureReason,
		&c.TemplateID, &c.TemplateVersion, &c.FromEmail, &c.FromName, &c.ReplyTo, &audience,
		&c.ScheduledAt, &c.StartedAt, &c.CompletedAt, &c.ResumeAfter,
		&c.Counters.Targeted, &c.Counters.Accepted, &c.Counters.Suppressed, &c.Counters.Sent,
		&c.Counters.Delivered, &c.Counters.Bounced, &c.Counters.Complained, &c.Counters.Opened,
		&c.Counters.Clicked, &c.Counters.Unsubscribed, &c.Counters.Failed,
		&c.CreatedBy, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrCampaignNotFound
		}
		return nil, err
	}
	c.Status = domain.Status(status)
	if err := json.Unmarshal(audience, &c.Audience); err != nil {
		return nil, fmt.Errorf("audiencia ilegible en la campana %s: %w", c.ID, err)
	}
	c.Audience = c.Audience.Normalized()
	return &c, nil
}

func collectCampaigns(rows pgx.Rows) ([]domain.Campaign, error) {
	defer rows.Close()
	var out []domain.Campaign
	for rows.Next() {
		c, err := scanCampaign(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func mapWriteError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == nameIndex {
		return domain.ErrNameTaken
	}
	return err
}

func (r *CampaignRepository) Insert(ctx context.Context, c *domain.Campaign) error {
	audience, err := json.Marshal(c.Audience.Normalized())
	if err != nil {
		return err
	}
	err = r.pool.QueryRow(ctx,
		`INSERT INTO campaigns.campaigns (id, tenant_id, name, description, status, template_id, from_email, from_name, reply_to, audience, created_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		 RETURNING created_at, updated_at`,
		c.ID, c.TenantID, c.Name, c.Description, string(c.Status), c.TemplateID, c.FromEmail, c.FromName,
		c.ReplyTo, audience, c.CreatedBy,
	).Scan(&c.CreatedAt, &c.UpdatedAt)
	return mapWriteError(err)
}

func (r *CampaignRepository) Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.Campaign, error) {
	return scanCampaign(r.pool.QueryRow(ctx,
		`SELECT `+campaignColumns+` FROM campaigns.campaigns WHERE tenant_id = $1 AND id = $2`, tenantID, id))
}

func (r *CampaignRepository) GetForUpdate(ctx context.Context, tenantID, id uuid.UUID) (*domain.Campaign, error) {
	return scanCampaign(r.pool.QueryRow(ctx,
		`SELECT `+campaignColumns+` FROM campaigns.campaigns WHERE tenant_id = $1 AND id = $2 FOR UPDATE`, tenantID, id))
}

// LockRunnable usa FOR NO KEY UPDATE y no FOR UPDATE: excluye igual a los demas
// orquestadores (NO KEY UPDATE choca consigo mismo), pero no a las comprobaciones de
// clave foranea de message_engagement, que toman KEY SHARE sobre la campana cada vez que
// llega una apertura. Con FOR UPDATE, una rafaga de aperturas haria que el orquestador
// saltara la campana tick tras tick.
func (r *CampaignRepository) LockRunnable(ctx context.Context, tenantID, id uuid.UUID, now time.Time) (*domain.Campaign, error) {
	c, err := scanCampaign(r.pool.QueryRow(ctx,
		`SELECT `+campaignColumns+` FROM campaigns.campaigns
		  WHERE tenant_id = $1 AND id = $2 AND status = $3 AND (resume_after IS NULL OR resume_after <= $4)
		  FOR NO KEY UPDATE SKIP LOCKED`,
		tenantID, id, string(domain.StatusSending), now))
	if errors.Is(err, domain.ErrCampaignNotFound) {
		return nil, nil
	}
	return c, err
}

func (r *CampaignRepository) Update(ctx context.Context, c *domain.Campaign) error {
	audience, err := json.Marshal(c.Audience.Normalized())
	if err != nil {
		return err
	}
	err = r.pool.QueryRow(ctx,
		`UPDATE campaigns.campaigns
		    SET name = $3, description = $4, status = $5, pause_reason = $6, failure_reason = $7,
		        template_id = $8, template_version = $9, from_email = $10, from_name = $11, reply_to = $12,
		        audience = $13, scheduled_at = $14, started_at = $15, completed_at = $16, resume_after = $17
		  WHERE tenant_id = $1 AND id = $2
		  RETURNING updated_at`,
		c.TenantID, c.ID, c.Name, c.Description, string(c.Status), c.PauseReason, c.FailureReason,
		c.TemplateID, c.TemplateVersion, c.FromEmail, c.FromName, c.ReplyTo,
		audience, c.ScheduledAt, c.StartedAt, c.CompletedAt, c.ResumeAfter,
	).Scan(&c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrCampaignNotFound
	}
	return mapWriteError(err)
}

func (r *CampaignRepository) Delete(ctx context.Context, tenantID, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM campaigns.campaigns WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrCampaignNotFound
	}
	return nil
}

// escapeLike neutraliza los comodines de LIKE en la busqueda del usuario.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func (r *CampaignRepository) List(ctx context.Context, tenantID uuid.UUID, f ports.ListFilter) ([]domain.Campaign, int64, error) {
	where := `WHERE tenant_id = $1 AND ($2 = '' OR status = $2) AND ($3 = '' OR name ILIKE '%' || $3 || '%')`
	args := []interface{}{tenantID, string(f.Status), escapeLike(strings.TrimSpace(f.Search))}

	var total int64
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM campaigns.campaigns `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.pool.Query(ctx,
		`SELECT `+campaignColumns+` FROM campaigns.campaigns `+where+` ORDER BY created_at DESC, id LIMIT $4 OFFSET $5`,
		append(args, f.PerPage, (f.Page-1)*f.PerPage)...)
	if err != nil {
		return nil, 0, err
	}
	out, err := collectCampaigns(rows)
	if err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// StartDue es la transicion scheduled -> sending del orquestador, hecha en una sola
// sentencia para todas las vencidas. SKIP LOCKED: si otra replica o una persona tiene la
// campana, se queda para el siguiente tick.
func (r *CampaignRepository) StartDue(ctx context.Context, tenantID uuid.UUID, now time.Time, limit int) ([]domain.Campaign, error) {
	rows, err := r.pool.Query(ctx,
		`WITH due AS (
			SELECT id FROM campaigns.campaigns
			 WHERE tenant_id = $1 AND status = $2 AND scheduled_at <= $3
			 ORDER BY scheduled_at LIMIT $4
			 FOR NO KEY UPDATE SKIP LOCKED
		)
		UPDATE campaigns.campaigns c
		   SET status = $5, started_at = COALESCE(c.started_at, $3), resume_after = NULL
		  FROM due WHERE c.id = due.id
		RETURNING `+qualified("c"),
		tenantID, string(domain.StatusScheduled), now, limit, string(domain.StatusSending))
	if err != nil {
		return nil, err
	}
	return collectCampaigns(rows)
}

func (r *CampaignRepository) ListRunnable(ctx context.Context, tenantID uuid.UUID, now time.Time, limit int) ([]uuid.UUID, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id FROM campaigns.campaigns
		  WHERE tenant_id = $1 AND status = $2 AND (resume_after IS NULL OR resume_after <= $3)
		  ORDER BY started_at NULLS FIRST, id LIMIT $4`,
		tenantID, string(domain.StatusSending), now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (r *CampaignRepository) AddDeliveryTotals(ctx context.Context, tenantID, id uuid.UUID, targeted, accepted, suppressed int) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE campaigns.campaigns
		    SET targeted = targeted + $3, accepted = accepted + $4, suppressed = suppressed + $5
		  WHERE tenant_id = $1 AND id = $2`,
		tenantID, id, targeted, accepted, suppressed)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrCampaignNotFound
	}
	return nil
}
