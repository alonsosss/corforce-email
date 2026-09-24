package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
)

// FilterRepo guarda mail.mailbox_filters: las reglas y el reenvio como jsonb y el script generado,
// que Dovecot lee por mail.v_sieve_user. Filtra por tenant_id ademas de la RLS del Transactor.
type FilterRepo struct{ pool *db.ContextPool }

func NewFilterRepo(pool *db.ContextPool) *FilterRepo { return &FilterRepo{pool: pool} }

func (r *FilterRepo) ByUsername(ctx context.Context, tenantID uuid.UUID, username string) (*domain.MailboxFilters, error) {
	var f domain.MailboxFilters
	var rules, forwarding []byte
	err := r.pool.QueryRow(ctx,
		`SELECT id, tenant_id, username, rules, forwarding, script_data, created_at, updated_at
		   FROM mail.mailbox_filters WHERE tenant_id = $1 AND username = $2`,
		tenantID, username,
	).Scan(&f.ID, &f.TenantID, &f.Username, &rules, &forwarding, &f.ScriptData, &f.CreatedAt, &f.UpdatedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	if err := json.Unmarshal(rules, &f.Rules); err != nil {
		return nil, fmt.Errorf("reglas de %s ilegibles: %w", username, err)
	}
	if err := json.Unmarshal(forwarding, &f.Forwarding); err != nil {
		return nil, fmt.Errorf("reenvio de %s ilegible: %w", username, err)
	}
	if f.Rules == nil {
		f.Rules = []domain.FilterRule{}
	}
	if f.Forwarding.Addresses == nil {
		f.Forwarding.Addresses = []string{}
	}
	return &f, nil
}

// Upsert crea o reemplaza las reglas del buzon enteras; solo la misma empresa reemplaza su fila.
func (r *FilterRepo) Upsert(ctx context.Context, f *domain.MailboxFilters) error {
	rules, err := json.Marshal(f.Rules)
	if err != nil {
		return err
	}
	forwarding, err := json.Marshal(f.Forwarding)
	if err != nil {
		return err
	}
	return mapErr(r.pool.QueryRow(ctx,
		`INSERT INTO mail.mailbox_filters (id, tenant_id, username, rules, forwarding, script_data)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (username) DO UPDATE
		    SET rules = EXCLUDED.rules, forwarding = EXCLUDED.forwarding, script_data = EXCLUDED.script_data
		  WHERE mail.mailbox_filters.tenant_id = EXCLUDED.tenant_id
		 RETURNING id, created_at, updated_at`,
		f.ID, f.TenantID, f.Username, rules, forwarding, f.ScriptData,
	).Scan(&f.ID, &f.CreatedAt, &f.UpdatedAt))
}

func (r *FilterRepo) DeleteByUsername(ctx context.Context, tenantID uuid.UUID, username string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM mail.mailbox_filters WHERE tenant_id = $1 AND username = $2`, tenantID, username)
	return err
}
