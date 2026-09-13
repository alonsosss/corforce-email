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

type SettingsRepository struct {
	pool *db.ContextPool
}

func NewSettingsRepository(pool *db.ContextPool) *SettingsRepository {
	return &SettingsRepository{pool: pool}
}

func (r *SettingsRepository) GetDOISettings(ctx context.Context, tenantID uuid.UUID) (*domain.DOISettings, error) {
	var (
		s         domain.DOISettings
		updatedAt time.Time
	)
	err := r.pool.QueryRow(ctx,
		`SELECT tenant_id, enabled, template_id, from_email, from_name, reply_to, updated_by, updated_at
		   FROM automations.doi_settings WHERE tenant_id = $1`, tenantID,
	).Scan(&s.TenantID, &s.Enabled, &s.TemplateID, &s.FromEmail, &s.FromName, &s.ReplyTo, &s.UpdatedBy, &updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s.UpdatedAt = &updatedAt
	return &s, nil
}

func (r *SettingsRepository) UpsertDOISettings(ctx context.Context, s *domain.DOISettings) error {
	var updatedAt time.Time
	err := r.pool.QueryRow(ctx,
		`INSERT INTO automations.doi_settings (tenant_id, enabled, template_id, from_email, from_name, reply_to, updated_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 ON CONFLICT (tenant_id) DO UPDATE
		    SET enabled = EXCLUDED.enabled, template_id = EXCLUDED.template_id, from_email = EXCLUDED.from_email,
		        from_name = EXCLUDED.from_name, reply_to = EXCLUDED.reply_to, updated_by = EXCLUDED.updated_by
		 RETURNING updated_at`,
		s.TenantID, s.Enabled, s.TemplateID, s.FromEmail, s.FromName, s.ReplyTo, s.UpdatedBy,
	).Scan(&updatedAt)
	if err != nil {
		return err
	}
	s.UpdatedAt = &updatedAt
	return nil
}
