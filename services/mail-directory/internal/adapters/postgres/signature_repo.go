package postgres

import (
	"context"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
)

// SignatureRepo guarda mail.mailbox_signatures. Filtra por tenant_id ademas de la RLS del Transactor.
type SignatureRepo struct{ pool *db.ContextPool }

func NewSignatureRepo(pool *db.ContextPool) *SignatureRepo { return &SignatureRepo{pool: pool} }

func (r *SignatureRepo) ByUsername(ctx context.Context, tenantID uuid.UUID, username string) (*domain.MailboxSignature, error) {
	var s domain.MailboxSignature
	err := r.pool.QueryRow(ctx,
		`SELECT id, tenant_id, username, enabled, html, text, on_replies, created_at, updated_at
		   FROM mail.mailbox_signatures WHERE tenant_id = $1 AND username = $2`,
		tenantID, username,
	).Scan(&s.ID, &s.TenantID, &s.Username, &s.Enabled, &s.HTML, &s.Text, &s.OnReplies, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	return &s, nil
}

// Upsert crea la firma o reemplaza la del buzon entera; solo la misma empresa reemplaza su fila.
func (r *SignatureRepo) Upsert(ctx context.Context, s *domain.MailboxSignature) error {
	return mapErr(r.pool.QueryRow(ctx,
		`INSERT INTO mail.mailbox_signatures (id, tenant_id, username, enabled, html, text, on_replies)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 ON CONFLICT (username) DO UPDATE
		    SET enabled = EXCLUDED.enabled, html = EXCLUDED.html, text = EXCLUDED.text, on_replies = EXCLUDED.on_replies
		  WHERE mail.mailbox_signatures.tenant_id = EXCLUDED.tenant_id
		 RETURNING id, created_at, updated_at`,
		s.ID, s.TenantID, s.Username, s.Enabled, s.HTML, s.Text, s.OnReplies,
	).Scan(&s.ID, &s.CreatedAt, &s.UpdatedAt))
}

func (r *SignatureRepo) DeleteByUsername(ctx context.Context, tenantID uuid.UUID, username string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM mail.mailbox_signatures WHERE tenant_id = $1 AND username = $2`, tenantID, username)
	return err
}
