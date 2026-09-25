package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/keyrotation"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MFARepo guarda mail.mailbox_mfa (migracion 16). Filtra por tenant_id ademas de la RLS del Transactor.
type MFARepo struct{ pool *db.ContextPool }

func NewMFARepo(pool *db.ContextPool) *MFARepo { return &MFARepo{pool: pool} }

func (r *MFARepo) Get(ctx context.Context, tenantID, mailboxID uuid.UUID) (*domain.MailboxMFA, error) {
	var m domain.MailboxMFA
	err := r.pool.QueryRow(ctx,
		`SELECT mailbox_id, tenant_id, secret_enc, recovery_hashes, last_step, enabled_at, created_at, updated_at
		   FROM mail.mailbox_mfa WHERE tenant_id = $1 AND mailbox_id = $2`,
		tenantID, mailboxID,
	).Scan(&m.MailboxID, &m.TenantID, &m.SecretEnc, &m.RecoveryHashes, &m.LastStep, &m.EnabledAt, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	return &m, nil
}

func (r *MFARepo) Create(ctx context.Context, m *domain.MailboxMFA) error {
	return mapErr(r.pool.QueryRow(ctx,
		`INSERT INTO mail.mailbox_mfa (mailbox_id, tenant_id, secret_enc, recovery_hashes, last_step, enabled_at)
		 VALUES ($1, $2, $3, $4, $5, $6) RETURNING enabled_at, created_at, updated_at`,
		m.MailboxID, m.TenantID, m.SecretEnc, m.RecoveryHashes, m.LastStep, m.EnabledAt,
	).Scan(&m.EnabledAt, &m.CreatedAt, &m.UpdatedAt))
}

func (r *MFARepo) AdvanceStep(ctx context.Context, tenantID, mailboxID uuid.UUID, step int64) (bool, error) {
	tag, err := r.pool.Exec(ctx,
		`UPDATE mail.mailbox_mfa SET last_step = $3 WHERE tenant_id = $1 AND mailbox_id = $2 AND last_step < $3`,
		tenantID, mailboxID, step)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (r *MFARepo) ConsumeRecoveryCode(ctx context.Context, tenantID, mailboxID uuid.UUID, hash string) (int, bool, error) {
	var remaining int
	err := r.pool.QueryRow(ctx,
		`UPDATE mail.mailbox_mfa SET recovery_hashes = array_remove(recovery_hashes, $3)
		  WHERE tenant_id = $1 AND mailbox_id = $2 AND $3 = ANY(recovery_hashes)
		 RETURNING cardinality(recovery_hashes)`,
		tenantID, mailboxID, hash,
	).Scan(&remaining)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return remaining, true, nil
}

func (r *MFARepo) ReplaceRecoveryCodes(ctx context.Context, tenantID, mailboxID uuid.UUID, hashes []string) error {
	return affected(r.pool.Exec(ctx,
		`UPDATE mail.mailbox_mfa SET recovery_hashes = $3 WHERE tenant_id = $1 AND mailbox_id = $2`,
		tenantID, mailboxID, hashes))
}

func (r *MFARepo) Delete(ctx context.Context, tenantID, mailboxID uuid.UUID) (bool, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM mail.mailbox_mfa WHERE tenant_id = $1 AND mailbox_id = $2`, tenantID, mailboxID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// NewMFASecretStore es la columna del secreto TOTP cifrado de mail.mailbox_mfa, de todas las empresas de
// la celda, para re-cifrarla bajo la llave activa. Es mantenimiento de la celda, sin usuario detras:
// corre con el rol de conexion del servicio (politica service_all), no con mail_app.
func NewMFASecretStore(pool *pgxpool.Pool) *keyrotation.Column {
	return keyrotation.MustColumn(pool, "mail.mailbox_mfa", "mailbox_id", "secret_enc")
}

// policyLockSpace es la primera mitad de la clave del cerrojo de la politica de correo de una empresa
// ("mdpl"), en el mismo espacio de dos enteros que el de la baja ("mdrt") y sin chocar con el.
const policyLockSpace int32 = 0x6d64706c

// MailPolicyRepo guarda mail.mail_policy (migracion 16).
type MailPolicyRepo struct{ pool *db.ContextPool }

func NewMailPolicyRepo(pool *db.ContextPool) *MailPolicyRepo { return &MailPolicyRepo{pool: pool} }

func (r *MailPolicyRepo) Lock(ctx context.Context, tenantID uuid.UUID, exclusive bool) error {
	sql := `SELECT pg_advisory_xact_lock_shared($1, hashtext($2))`
	if exclusive {
		sql = `SELECT pg_advisory_xact_lock($1, hashtext($2))`
	}
	if _, err := r.pool.Exec(ctx, sql, policyLockSpace, tenantID.String()); err != nil {
		return fmt.Errorf("cerrojo de la política de correo de la empresa %s: %w", tenantID, err)
	}
	return nil
}

func (r *MailPolicyRepo) Get(ctx context.Context, tenantID uuid.UUID) (*domain.MailPolicy, error) {
	var p domain.MailPolicy
	err := r.pool.QueryRow(ctx,
		`SELECT tenant_id, external_forwarding_allowed, updated_by, updated_at FROM mail.mail_policy WHERE tenant_id = $1`,
		tenantID,
	).Scan(&p.TenantID, &p.ExternalForwardingAllowed, &p.UpdatedBy, &p.UpdatedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	return &p, nil
}

func (r *MailPolicyRepo) Upsert(ctx context.Context, p *domain.MailPolicy) error {
	return mapErr(r.pool.QueryRow(ctx,
		`INSERT INTO mail.mail_policy (tenant_id, external_forwarding_allowed, updated_by)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (tenant_id) DO UPDATE
		    SET external_forwarding_allowed = EXCLUDED.external_forwarding_allowed, updated_by = EXCLUDED.updated_by
		 RETURNING updated_at`,
		p.TenantID, p.ExternalForwardingAllowed, p.UpdatedBy,
	).Scan(&p.UpdatedAt))
}
