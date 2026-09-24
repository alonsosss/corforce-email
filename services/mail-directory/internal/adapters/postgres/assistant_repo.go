package postgres

import (
	"context"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
)

// AssistantSettingsRepo guarda mail.assistant_settings. Filtra por tenant_id ademas de la RLS del
// Transactor.
type AssistantSettingsRepo struct{ pool *db.ContextPool }

func NewAssistantSettingsRepo(pool *db.ContextPool) *AssistantSettingsRepo {
	return &AssistantSettingsRepo{pool: pool}
}

func (r *AssistantSettingsRepo) Get(ctx context.Context, tenantID uuid.UUID) (*domain.AssistantSettings, error) {
	var s domain.AssistantSettings
	err := r.pool.QueryRow(ctx,
		`SELECT tenant_id, enabled, updated_by, enabled_at, updated_at
		   FROM mail.assistant_settings WHERE tenant_id = $1`,
		tenantID,
	).Scan(&s.TenantID, &s.Enabled, &s.UpdatedBy, &s.EnabledAt, &s.UpdatedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	return &s, nil
}

// Upsert crea la fila de la empresa o la reemplaza entera.
func (r *AssistantSettingsRepo) Upsert(ctx context.Context, s *domain.AssistantSettings) error {
	return mapErr(r.pool.QueryRow(ctx,
		`INSERT INTO mail.assistant_settings (tenant_id, enabled, updated_by, enabled_at)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (tenant_id) DO UPDATE
		    SET enabled = EXCLUDED.enabled, updated_by = EXCLUDED.updated_by, enabled_at = EXCLUDED.enabled_at
		 RETURNING updated_at`,
		s.TenantID, s.Enabled, s.UpdatedBy, s.EnabledAt,
	).Scan(&s.UpdatedAt))
}
