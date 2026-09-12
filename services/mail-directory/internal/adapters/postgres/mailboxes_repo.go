package postgres

import (
	"context"
	"net/netip"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type MailboxRepo struct {
	pool *db.ContextPool
}

func NewMailboxRepo(pool *db.ContextPool) *MailboxRepo { return &MailboxRepo{pool: pool} }

// mailboxColumns no incluye password_hash: solo lo escribe este servicio y lo lee mail-auth.
const mailboxColumns = `id, tenant_id, username, local_part, domain, display_name, quota_bytes, active, kind,
 tls_enforce_in, tls_enforce_out, relayhost_id, imap_access, pop3_access, smtp_access, sieve_access,
 force_pw_update, created_at, updated_at`

func scanMailbox(row pgx.Row) (domain.Mailbox, error) {
	var m domain.Mailbox
	err := row.Scan(&m.ID, &m.TenantID, &m.Username, &m.LocalPart, &m.Domain, &m.DisplayName, &m.QuotaBytes, &m.Active,
		&m.Kind, &m.TLSEnforceIn, &m.TLSEnforceOut, &m.RelayhostID, &m.IMAPAccess, &m.POP3Access, &m.SMTPAccess,
		&m.SieveAccess, &m.ForcePwUpdate, &m.CreatedAt, &m.UpdatedAt)
	return m, mapErr(err)
}

func (r *MailboxRepo) List(ctx context.Context, tenantID uuid.UUID, page ports.Page) ([]domain.Mailbox, int64, error) {
	return listPage(ctx, r.pool,
		`SELECT COUNT(*) FROM mail.mailboxes WHERE tenant_id = $1`,
		`SELECT `+mailboxColumns+` FROM mail.mailboxes WHERE tenant_id = $1 ORDER BY username LIMIT $2 OFFSET $3`,
		scanMailbox, page.Limit, page.Offset, tenantID)
}

func (r *MailboxRepo) Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.Mailbox, error) {
	m, err := scanMailbox(r.pool.QueryRow(ctx,
		`SELECT `+mailboxColumns+` FROM mail.mailboxes WHERE tenant_id = $1 AND id = $2`, tenantID, id))
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *MailboxRepo) GetByUsername(ctx context.Context, tenantID uuid.UUID, username string) (*domain.Mailbox, error) {
	m, err := scanMailbox(r.pool.QueryRow(ctx,
		`SELECT `+mailboxColumns+` FROM mail.mailboxes WHERE tenant_id = $1 AND username = $2`, tenantID, username))
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *MailboxRepo) Create(ctx context.Context, m *domain.Mailbox) error {
	return mapErr(r.pool.QueryRow(ctx,
		`INSERT INTO mail.mailboxes (id, tenant_id, username, local_part, domain, password_hash, display_name, quota_bytes,
 active, kind, tls_enforce_in, tls_enforce_out, relayhost_id, imap_access, pop3_access, smtp_access, sieve_access, force_pw_update)
 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18) RETURNING created_at, updated_at`,
		m.ID, m.TenantID, m.Username, m.LocalPart, m.Domain, m.PasswordHash, m.DisplayName, m.QuotaBytes,
		m.Active, m.Kind, m.TLSEnforceIn, m.TLSEnforceOut, m.RelayhostID, m.IMAPAccess, m.POP3Access, m.SMTPAccess,
		m.SieveAccess, m.ForcePwUpdate,
	).Scan(&m.CreatedAt, &m.UpdatedAt))
}

func (r *MailboxRepo) Update(ctx context.Context, m *domain.Mailbox) error {
	return mapErr(r.pool.QueryRow(ctx,
		`UPDATE mail.mailboxes SET display_name = $3, quota_bytes = $4, active = $5, kind = $6, tls_enforce_in = $7,
 tls_enforce_out = $8, relayhost_id = $9, imap_access = $10, pop3_access = $11, smtp_access = $12, sieve_access = $13,
 force_pw_update = $14
 WHERE tenant_id = $1 AND id = $2 RETURNING updated_at`,
		m.TenantID, m.ID, m.DisplayName, m.QuotaBytes, m.Active, m.Kind, m.TLSEnforceIn, m.TLSEnforceOut,
		m.RelayhostID, m.IMAPAccess, m.POP3Access, m.SMTPAccess, m.SieveAccess, m.ForcePwUpdate,
	).Scan(&m.UpdatedAt))
}

// UpdatePassword apaga force_pw_update: la contrasena ya se cambio.
func (r *MailboxRepo) UpdatePassword(ctx context.Context, tenantID, id uuid.UUID, hash string) error {
	return affected(r.pool.Exec(ctx,
		`UPDATE mail.mailboxes SET password_hash = $3, force_pw_update = false WHERE tenant_id = $1 AND id = $2`,
		tenantID, id, hash))
}

func (r *MailboxRepo) Delete(ctx context.Context, tenantID, id uuid.UUID) error {
	return affected(r.pool.Exec(ctx, `DELETE FROM mail.mailboxes WHERE tenant_id = $1 AND id = $2`, tenantID, id))
}

func (r *MailboxRepo) CountByDomain(ctx context.Context, tenantID uuid.UUID, name string) (int64, error) {
	var n int64
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM mail.mailboxes WHERE tenant_id = $1 AND domain = $2`, tenantID, name).Scan(&n)
	return n, err
}

func (r *MailboxRepo) QuotaSumByDomain(ctx context.Context, tenantID uuid.UUID, name string, exclude uuid.UUID) (int64, error) {
	var sum int64
	err := r.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(quota_bytes), 0) FROM mail.mailboxes WHERE tenant_id = $1 AND domain = $2 AND id <> $3`,
		tenantID, name, exclude).Scan(&sum)
	return sum, err
}

// Quota une el uso que escribe Dovecot con el buzon de la empresa: quota_usage no tiene
// tenant_id y solo se lee a traves de mailboxes.
func (r *MailboxRepo) Quota(ctx context.Context, tenantID, id uuid.UUID) (*domain.QuotaUsage, error) {
	var q domain.QuotaUsage
	err := r.pool.QueryRow(ctx,
		`SELECT m.quota_bytes, COALESCE(q.bytes, 0), COALESCE(q.messages, 0)
   FROM mail.mailboxes m LEFT JOIN mail.quota_usage q ON q.username = m.username
  WHERE m.tenant_id = $1 AND m.id = $2`,
		tenantID, id).Scan(&q.QuotaBytes, &q.UsedBytes, &q.Messages)
	if err != nil {
		return nil, mapErr(err)
	}
	return &q, nil
}

func (r *MailboxRepo) DeleteQuotaUsage(ctx context.Context, tenantID uuid.UUID, username string) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM mail.quota_usage WHERE username = $2
    AND EXISTS (SELECT 1 FROM mail.mailboxes m WHERE m.tenant_id = $1 AND m.username = $2)`,
		tenantID, username)
	return err
}

func (r *MailboxRepo) Logins(ctx context.Context, tenantID uuid.UUID, username string, limit int) ([]domain.SASLLogin, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, username, service, app_password_id, remote_ip, logged_at FROM mail.sasl_logins
  WHERE tenant_id = $1 AND username = $2 ORDER BY logged_at DESC LIMIT $3`,
		tenantID, username, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	logins := make([]domain.SASLLogin, 0, limit)
	for rows.Next() {
		var l domain.SASLLogin
		var ip *netip.Addr
		if err := rows.Scan(&l.ID, &l.Username, &l.Service, &l.AppPasswordID, &ip, &l.LoggedAt); err != nil {
			return nil, err
		}
		if ip != nil {
			l.RemoteIP = ip.String()
		}
		logins = append(logins, l)
	}
	return logins, rows.Err()
}

func (r *MailboxRepo) AddressInUse(ctx context.Context, tenantID uuid.UUID, address string) (bool, error) {
	var inUse bool
	err := r.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM mail.mailboxes WHERE tenant_id = $1 AND username = $2)
        OR EXISTS (SELECT 1 FROM mail.aliases WHERE tenant_id = $1 AND address = $2)
        OR EXISTS (SELECT 1 FROM mail.spam_aliases WHERE tenant_id = $1 AND address = $2)`,
		tenantID, address).Scan(&inUse)
	return inUse, err
}

// ── Contrasenas de aplicacion ─────────────────────────────────────────────────

type AppPasswordRepo struct {
	pool *db.ContextPool
}

func NewAppPasswordRepo(pool *db.ContextPool) *AppPasswordRepo { return &AppPasswordRepo{pool: pool} }

const appPasswordColumns = `id, tenant_id, mailbox_id, name, imap_access, pop3_access, smtp_access, sieve_access,
 dav_access, active, last_used_at, created_at, updated_at`

func scanAppPassword(row pgx.Row) (domain.AppPassword, error) {
	var p domain.AppPassword
	err := row.Scan(&p.ID, &p.TenantID, &p.MailboxID, &p.Name, &p.IMAPAccess, &p.POP3Access, &p.SMTPAccess,
		&p.SieveAccess, &p.DAVAccess, &p.Active, &p.LastUsedAt, &p.CreatedAt, &p.UpdatedAt)
	return p, mapErr(err)
}

func (r *AppPasswordRepo) List(ctx context.Context, tenantID, mailboxID uuid.UUID) ([]domain.AppPassword, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+appPasswordColumns+` FROM mail.app_passwords WHERE tenant_id = $1 AND mailbox_id = $2 ORDER BY created_at`,
		tenantID, mailboxID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []domain.AppPassword
	for rows.Next() {
		p, err := scanAppPassword(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, p)
	}
	return items, rows.Err()
}

func (r *AppPasswordRepo) Get(ctx context.Context, tenantID, mailboxID, id uuid.UUID) (*domain.AppPassword, error) {
	p, err := scanAppPassword(r.pool.QueryRow(ctx,
		`SELECT `+appPasswordColumns+` FROM mail.app_passwords WHERE tenant_id = $1 AND mailbox_id = $2 AND id = $3`,
		tenantID, mailboxID, id))
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *AppPasswordRepo) Create(ctx context.Context, p *domain.AppPassword) error {
	return mapErr(r.pool.QueryRow(ctx,
		`INSERT INTO mail.app_passwords (id, tenant_id, mailbox_id, name, password_hash, imap_access, pop3_access,
 smtp_access, sieve_access, dav_access, active)
 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING created_at, updated_at`,
		p.ID, p.TenantID, p.MailboxID, p.Name, p.PasswordHash, p.IMAPAccess, p.POP3Access,
		p.SMTPAccess, p.SieveAccess, p.DAVAccess, p.Active,
	).Scan(&p.CreatedAt, &p.UpdatedAt))
}

func (r *AppPasswordRepo) Update(ctx context.Context, p *domain.AppPassword) error {
	return mapErr(r.pool.QueryRow(ctx,
		`UPDATE mail.app_passwords SET name = $4, imap_access = $5, pop3_access = $6, smtp_access = $7, sieve_access = $8,
 dav_access = $9, active = $10 WHERE tenant_id = $1 AND mailbox_id = $2 AND id = $3 RETURNING updated_at`,
		p.TenantID, p.MailboxID, p.ID, p.Name, p.IMAPAccess, p.POP3Access, p.SMTPAccess, p.SieveAccess, p.DAVAccess, p.Active,
	).Scan(&p.UpdatedAt))
}

func (r *AppPasswordRepo) Delete(ctx context.Context, tenantID, mailboxID, id uuid.UUID) error {
	return affected(r.pool.Exec(ctx,
		`DELETE FROM mail.app_passwords WHERE tenant_id = $1 AND mailbox_id = $2 AND id = $3`, tenantID, mailboxID, id))
}

func (r *AppPasswordRepo) DeleteByMailbox(ctx context.Context, tenantID, mailboxID uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM mail.app_passwords WHERE tenant_id = $1 AND mailbox_id = $2`, tenantID, mailboxID)
	return err
}

func (r *AppPasswordRepo) DeactivateByMailbox(ctx context.Context, tenantID, mailboxID uuid.UUID) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE mail.app_passwords SET active = false WHERE tenant_id = $1 AND mailbox_id = $2 AND active`, tenantID, mailboxID)
	return err
}

// ── Sieve ─────────────────────────────────────────────────────────────────────

type SieveRepo struct {
	pool *db.ContextPool
}

func NewSieveRepo(pool *db.ContextPool) *SieveRepo { return &SieveRepo{pool: pool} }

func scriptName(active bool) string {
	if active {
		return "active"
	}
	return "inactive"
}

func (r *SieveRepo) ByUsername(ctx context.Context, tenantID uuid.UUID, username string) ([]domain.SieveFilter, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, tenant_id, username, filter_type, script_desc, script_data, script_name = 'active', created_at, updated_at
   FROM mail.sieve_filters WHERE tenant_id = $1 AND username = $2 ORDER BY filter_type`,
		tenantID, username)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []domain.SieveFilter
	for rows.Next() {
		var f domain.SieveFilter
		if err := rows.Scan(&f.ID, &f.TenantID, &f.Username, &f.FilterType, &f.ScriptDesc, &f.ScriptData, &f.Active,
			&f.CreatedAt, &f.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, f)
	}
	return items, rows.Err()
}

// Replace borra el filtro previo de ese tipo e inserta el nuevo en la misma transaccion:
// Dovecot lee por username y tipo, y nunca debe encontrar dos.
func (r *SieveRepo) Replace(ctx context.Context, tenantID uuid.UUID, username, filterType string, f *domain.SieveFilter) error {
	if _, err := r.pool.Exec(ctx,
		`DELETE FROM mail.sieve_filters WHERE tenant_id = $1 AND username = $2 AND filter_type = $3`,
		tenantID, username, filterType); err != nil {
		return err
	}
	if f == nil {
		return nil
	}
	return mapErr(r.pool.QueryRow(ctx,
		`INSERT INTO mail.sieve_filters (id, tenant_id, username, script_desc, script_name, script_data, filter_type)
 VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING created_at, updated_at`,
		f.ID, f.TenantID, f.Username, f.ScriptDesc, scriptName(f.Active), f.ScriptData, f.FilterType,
	).Scan(&f.CreatedAt, &f.UpdatedAt))
}

func (r *SieveRepo) DeleteByUsername(ctx context.Context, tenantID uuid.UUID, username string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM mail.sieve_filters WHERE tenant_id = $1 AND username = $2`, tenantID, username)
	return err
}
