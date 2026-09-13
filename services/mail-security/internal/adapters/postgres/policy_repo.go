package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// PolicyRepository implementa ports.PolicyRepository sobre mail_security.*. Cada consulta
// lleva tenant_id ademas de la politica RLS: dos cerrojos, no uno.
type PolicyRepository struct {
	pool *db.ContextPool
}

func NewPolicyRepository(pool *db.ContextPool) *PolicyRepository { return &PolicyRepository{pool: pool} }

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	return err
}

// scanDecimal lee un numeric serializado como texto: pgx no conoce decimal.Decimal y
// pasar por float64 perderia precision y admitiria NaN.
func scanDecimal(s string) (decimal.Decimal, error) {
	if s == "" {
		return decimal.Zero, nil
	}
	return decimal.NewFromString(s)
}

// ── Umbrales ─────────────────────────────────────────────────────────────────

const spamScoreColumns = `id, tenant_id, object, high_score::text, low_score::text, created_at, updated_at`

func scanSpamScore(row pgx.Row) (*domain.SpamScore, error) {
	var s domain.SpamScore
	var high, low string
	if err := row.Scan(&s.ID, &s.TenantID, &s.Object, &high, &low, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return nil, err
	}
	var err error
	if s.HighScore, err = scanDecimal(high); err != nil {
		return nil, err
	}
	if s.LowScore, err = scanDecimal(low); err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *PolicyRepository) ListSpamScores(ctx context.Context, tenantID uuid.UUID) ([]domain.SpamScore, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+spamScoreColumns+` FROM mail_security.spam_scores WHERE tenant_id = $1 ORDER BY object`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.SpamScore{}
	for rows.Next() {
		s, err := scanSpamScore(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

func (r *PolicyRepository) GetSpamScore(ctx context.Context, tenantID uuid.UUID, object string) (*domain.SpamScore, error) {
	s, err := scanSpamScore(r.pool.QueryRow(ctx, `SELECT `+spamScoreColumns+` FROM mail_security.spam_scores WHERE tenant_id = $1 AND object = $2`, tenantID, object))
	return s, notFound(err)
}

func (r *PolicyRepository) UpsertSpamScore(ctx context.Context, s *domain.SpamScore) error {
	row := r.pool.QueryRow(ctx, `
		INSERT INTO mail_security.spam_scores (tenant_id, object, high_score, low_score)
		VALUES ($1, $2, $3::numeric, $4::numeric)
		ON CONFLICT (object) DO UPDATE
		   SET high_score = EXCLUDED.high_score, low_score = EXCLUDED.low_score
		 WHERE mail_security.spam_scores.tenant_id = EXCLUDED.tenant_id
		RETURNING `+spamScoreColumns, s.TenantID, s.Object, s.HighScore.String(), s.LowScore.String())
	saved, err := scanSpamScore(row)
	if err != nil {
		return notFound(err)
	}
	*s = *saved
	return nil
}

func (r *PolicyRepository) DeleteSpamScore(ctx context.Context, tenantID uuid.UUID, object string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM mail_security.spam_scores WHERE tenant_id = $1 AND object = $2`, tenantID, object)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// ── Listas ────────────────────────────────────────────────────────────────────

const addressListColumns = `id, tenant_id, object, kind, pattern, created_at, updated_at`

func (r *PolicyRepository) ListAddressLists(ctx context.Context, tenantID uuid.UUID, object string, kind domain.ListKind) ([]domain.AddressListEntry, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+addressListColumns+` FROM mail_security.address_lists
		 WHERE tenant_id = $1 AND ($2 = '' OR object = $2) AND ($3 = '' OR kind = $3)
		 ORDER BY object, kind, pattern`, tenantID, object, string(kind))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.AddressListEntry{}
	for rows.Next() {
		var e domain.AddressListEntry
		if err := rows.Scan(&e.ID, &e.TenantID, &e.Object, &e.Kind, &e.Pattern, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r *PolicyRepository) CreateAddressList(ctx context.Context, e *domain.AddressListEntry) error {
	return r.pool.QueryRow(ctx, `
		INSERT INTO mail_security.address_lists (tenant_id, object, kind, pattern)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (object, kind, pattern) DO UPDATE SET updated_at = now()
		 WHERE mail_security.address_lists.tenant_id = EXCLUDED.tenant_id
		RETURNING `+addressListColumns, e.TenantID, e.Object, string(e.Kind), e.Pattern,
	).Scan(&e.ID, &e.TenantID, &e.Object, &e.Kind, &e.Pattern, &e.CreatedAt, &e.UpdatedAt)
}

func (r *PolicyRepository) DeleteAddressList(ctx context.Context, tenantID, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM mail_security.address_lists WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// ── Bloques adicionales ───────────────────────────────────────────────────────

const settingsMapColumns = `id, tenant_id, description, content, active, created_at, updated_at`

func scanSettingsMap(row pgx.Row) (*domain.SettingsMap, error) {
	var m domain.SettingsMap
	if err := row.Scan(&m.ID, &m.TenantID, &m.Description, &m.Content, &m.Active, &m.CreatedAt, &m.UpdatedAt); err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *PolicyRepository) ListSettingsMaps(ctx context.Context, tenantID uuid.UUID) ([]domain.SettingsMap, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+settingsMapColumns+` FROM mail_security.settings_maps WHERE tenant_id = $1 ORDER BY created_at`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.SettingsMap{}
	for rows.Next() {
		m, err := scanSettingsMap(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

func (r *PolicyRepository) GetSettingsMap(ctx context.Context, tenantID, id uuid.UUID) (*domain.SettingsMap, error) {
	m, err := scanSettingsMap(r.pool.QueryRow(ctx, `SELECT `+settingsMapColumns+` FROM mail_security.settings_maps WHERE tenant_id = $1 AND id = $2`, tenantID, id))
	return m, notFound(err)
}

func (r *PolicyRepository) CreateSettingsMap(ctx context.Context, m *domain.SettingsMap) error {
	saved, err := scanSettingsMap(r.pool.QueryRow(ctx, `
		INSERT INTO mail_security.settings_maps (tenant_id, description, content, active)
		VALUES ($1, $2, $3, $4) RETURNING `+settingsMapColumns, m.TenantID, m.Description, m.Content, m.Active))
	if err != nil {
		return err
	}
	*m = *saved
	return nil
}

func (r *PolicyRepository) UpdateSettingsMap(ctx context.Context, m *domain.SettingsMap) error {
	saved, err := scanSettingsMap(r.pool.QueryRow(ctx, `
		UPDATE mail_security.settings_maps SET description = $3, content = $4, active = $5
		 WHERE tenant_id = $1 AND id = $2 RETURNING `+settingsMapColumns, m.TenantID, m.ID, m.Description, m.Content, m.Active))
	if err != nil {
		return notFound(err)
	}
	*m = *saved
	return nil
}

func (r *PolicyRepository) DeleteSettingsMap(ctx context.Context, tenantID, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM mail_security.settings_maps WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// ── Pies de pagina ────────────────────────────────────────────────────────────

const footerColumns = `id, tenant_id, domain, html, plain, mailbox_exclude, alias_domain_exclude, skip_replies, created_at, updated_at`

func scanFooter(row pgx.Row) (*domain.DomainFooter, error) {
	var f domain.DomainFooter
	if err := row.Scan(&f.ID, &f.TenantID, &f.Domain, &f.HTML, &f.Plain, &f.MailboxExclude, &f.AliasDomainExclude, &f.SkipReplies, &f.CreatedAt, &f.UpdatedAt); err != nil {
		return nil, err
	}
	if f.MailboxExclude == nil {
		f.MailboxExclude = []string{}
	}
	if f.AliasDomainExclude == nil {
		f.AliasDomainExclude = []string{}
	}
	return &f, nil
}

func (r *PolicyRepository) ListFooters(ctx context.Context, tenantID uuid.UUID) ([]domain.DomainFooter, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+footerColumns+` FROM mail_security.domain_footers WHERE tenant_id = $1 ORDER BY domain`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.DomainFooter{}
	for rows.Next() {
		f, err := scanFooter(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

func (r *PolicyRepository) GetFooter(ctx context.Context, tenantID uuid.UUID, domainName string) (*domain.DomainFooter, error) {
	f, err := scanFooter(r.pool.QueryRow(ctx, `SELECT `+footerColumns+` FROM mail_security.domain_footers WHERE tenant_id = $1 AND domain = $2`, tenantID, domainName))
	return f, notFound(err)
}

func (r *PolicyRepository) UpsertFooter(ctx context.Context, f *domain.DomainFooter) error {
	saved, err := scanFooter(r.pool.QueryRow(ctx, `
		INSERT INTO mail_security.domain_footers (tenant_id, domain, html, plain, mailbox_exclude, alias_domain_exclude, skip_replies)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (domain) DO UPDATE
		   SET html = EXCLUDED.html, plain = EXCLUDED.plain, mailbox_exclude = EXCLUDED.mailbox_exclude,
		       alias_domain_exclude = EXCLUDED.alias_domain_exclude, skip_replies = EXCLUDED.skip_replies
		 WHERE mail_security.domain_footers.tenant_id = EXCLUDED.tenant_id
		RETURNING `+footerColumns, f.TenantID, f.Domain, f.HTML, f.Plain, f.MailboxExclude, f.AliasDomainExclude, f.SkipReplies))
	if err != nil {
		return notFound(err)
	}
	*f = *saved
	return nil
}

func (r *PolicyRepository) DeleteFooter(ctx context.Context, tenantID uuid.UUID, domainName string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM mail_security.domain_footers WHERE tenant_id = $1 AND domain = $2`, tenantID, domainName)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// ── Hosts de reenvio ──────────────────────────────────────────────────────────

const forwardingHostColumns = `id, tenant_id, host::text, source, filter_spam, created_at, updated_at`

func scanForwardingHost(row pgx.Row) (*domain.ForwardingHost, error) {
	var h domain.ForwardingHost
	if err := row.Scan(&h.ID, &h.TenantID, &h.Host, &h.Source, &h.FilterSpam, &h.CreatedAt, &h.UpdatedAt); err != nil {
		return nil, err
	}
	return &h, nil
}

func (r *PolicyRepository) ListForwardingHosts(ctx context.Context, tenantID uuid.UUID) ([]domain.ForwardingHost, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+forwardingHostColumns+` FROM mail_security.forwarding_hosts WHERE tenant_id = $1 ORDER BY host`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.ForwardingHost{}
	for rows.Next() {
		h, err := scanForwardingHost(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *h)
	}
	return out, rows.Err()
}

func (r *PolicyRepository) GetForwardingHost(ctx context.Context, tenantID, id uuid.UUID) (*domain.ForwardingHost, error) {
	h, err := scanForwardingHost(r.pool.QueryRow(ctx, `SELECT `+forwardingHostColumns+` FROM mail_security.forwarding_hosts WHERE tenant_id = $1 AND id = $2`, tenantID, id))
	return h, notFound(err)
}

func (r *PolicyRepository) CreateForwardingHost(ctx context.Context, h *domain.ForwardingHost) error {
	saved, err := scanForwardingHost(r.pool.QueryRow(ctx, `
		INSERT INTO mail_security.forwarding_hosts (tenant_id, host, source, filter_spam)
		VALUES ($1, $2::cidr, $3, $4) RETURNING `+forwardingHostColumns, h.TenantID, h.Host, h.Source, h.FilterSpam))
	if err != nil {
		return err
	}
	*h = *saved
	return nil
}

func (r *PolicyRepository) DeleteForwardingHost(ctx context.Context, tenantID, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM mail_security.forwarding_hosts WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// ── Limites de envio ──────────────────────────────────────────────────────────

const rateLimitColumns = `id, tenant_id, object, value, created_at, updated_at`

func scanRateLimit(row pgx.Row) (*domain.RateLimit, error) {
	var rl domain.RateLimit
	if err := row.Scan(&rl.ID, &rl.TenantID, &rl.Object, &rl.Value, &rl.CreatedAt, &rl.UpdatedAt); err != nil {
		return nil, err
	}
	return &rl, nil
}

func (r *PolicyRepository) ListRateLimits(ctx context.Context, tenantID uuid.UUID) ([]domain.RateLimit, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+rateLimitColumns+` FROM mail_security.rate_limits WHERE tenant_id = $1 ORDER BY object`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.RateLimit{}
	for rows.Next() {
		rl, err := scanRateLimit(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *rl)
	}
	return out, rows.Err()
}

func (r *PolicyRepository) GetRateLimit(ctx context.Context, tenantID uuid.UUID, object string) (*domain.RateLimit, error) {
	rl, err := scanRateLimit(r.pool.QueryRow(ctx, `SELECT `+rateLimitColumns+` FROM mail_security.rate_limits WHERE tenant_id = $1 AND object = $2`, tenantID, object))
	return rl, notFound(err)
}

func (r *PolicyRepository) UpsertRateLimit(ctx context.Context, rl *domain.RateLimit) error {
	saved, err := scanRateLimit(r.pool.QueryRow(ctx, `
		INSERT INTO mail_security.rate_limits (tenant_id, object, value) VALUES ($1, $2, $3)
		ON CONFLICT (object) DO UPDATE SET value = EXCLUDED.value
		 WHERE mail_security.rate_limits.tenant_id = EXCLUDED.tenant_id
		RETURNING `+rateLimitColumns, rl.TenantID, rl.Object, rl.Value))
	if err != nil {
		return notFound(err)
	}
	*rl = *saved
	return nil
}

func (r *PolicyRepository) DeleteRateLimit(ctx context.Context, tenantID uuid.UUID, object string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM mail_security.rate_limits WHERE tenant_id = $1 AND object = $2`, tenantID, object)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// ── Etiquetas por buzon ───────────────────────────────────────────────────────

const mailboxTagsColumns = `id, tenant_id, username, subject_tag, subfolder_tag, created_at, updated_at`

func scanMailboxTags(row pgx.Row) (*domain.MailboxTags, error) {
	var t domain.MailboxTags
	if err := row.Scan(&t.ID, &t.TenantID, &t.Username, &t.SubjectTag, &t.SubfolderTag, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return nil, err
	}
	return &t, nil
}

func (r *PolicyRepository) ListMailboxTags(ctx context.Context, tenantID uuid.UUID) ([]domain.MailboxTags, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+mailboxTagsColumns+` FROM mail_security.mailbox_tags WHERE tenant_id = $1 ORDER BY username`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.MailboxTags{}
	for rows.Next() {
		t, err := scanMailboxTags(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func (r *PolicyRepository) GetMailboxTags(ctx context.Context, tenantID uuid.UUID, username string) (*domain.MailboxTags, error) {
	t, err := scanMailboxTags(r.pool.QueryRow(ctx, `SELECT `+mailboxTagsColumns+` FROM mail_security.mailbox_tags WHERE tenant_id = $1 AND username = $2`, tenantID, username))
	return t, notFound(err)
}

func (r *PolicyRepository) UpsertMailboxTags(ctx context.Context, t *domain.MailboxTags) error {
	saved, err := scanMailboxTags(r.pool.QueryRow(ctx, `
		INSERT INTO mail_security.mailbox_tags (tenant_id, username, subject_tag, subfolder_tag)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (username) DO UPDATE SET subject_tag = EXCLUDED.subject_tag, subfolder_tag = EXCLUDED.subfolder_tag
		 WHERE mail_security.mailbox_tags.tenant_id = EXCLUDED.tenant_id
		RETURNING `+mailboxTagsColumns, t.TenantID, t.Username, t.SubjectTag, t.SubfolderTag))
	if err != nil {
		return notFound(err)
	}
	*t = *saved
	return nil
}

// ── Ajustes de cuarentena ─────────────────────────────────────────────────────

const quarantineSettingsColumns = `tenant_id, max_size_bytes, max_age_days, retention_size, exclude_domains,
	notify_enabled, notify_max_score::text, notify_sender, notify_subject, notify_html_template, updated_at`

func scanQuarantineSettings(row pgx.Row) (*domain.QuarantineSettings, error) {
	var s domain.QuarantineSettings
	var maxScore string
	if err := row.Scan(&s.TenantID, &s.MaxSizeBytes, &s.MaxAgeDays, &s.RetentionSize, &s.ExcludeDomains,
		&s.Notify.Enabled, &maxScore, &s.Notify.Sender, &s.Notify.Subject, &s.Notify.HTMLTemplate, &s.UpdatedAt); err != nil {
		return nil, err
	}
	var err error
	if s.Notify.MaxScore, err = scanDecimal(maxScore); err != nil {
		return nil, err
	}
	if s.ExcludeDomains == nil {
		s.ExcludeDomains = []string{}
	}
	return &s, nil
}

func (r *PolicyRepository) GetQuarantineSettings(ctx context.Context, tenantID uuid.UUID) (*domain.QuarantineSettings, error) {
	s, err := scanQuarantineSettings(r.pool.QueryRow(ctx, `SELECT `+quarantineSettingsColumns+` FROM mail_security.quarantine_settings WHERE tenant_id = $1`, tenantID))
	return s, notFound(err)
}

func (r *PolicyRepository) UpsertQuarantineSettings(ctx context.Context, s *domain.QuarantineSettings) error {
	saved, err := scanQuarantineSettings(r.pool.QueryRow(ctx, `
		INSERT INTO mail_security.quarantine_settings
		    (tenant_id, max_size_bytes, max_age_days, retention_size, exclude_domains,
		     notify_enabled, notify_max_score, notify_sender, notify_subject, notify_html_template)
		VALUES ($1, $2, $3, $4, $5, $6, $7::numeric, $8, $9, $10)
		ON CONFLICT (tenant_id) DO UPDATE
		   SET max_size_bytes = EXCLUDED.max_size_bytes, max_age_days = EXCLUDED.max_age_days,
		       retention_size = EXCLUDED.retention_size, exclude_domains = EXCLUDED.exclude_domains,
		       notify_enabled = EXCLUDED.notify_enabled, notify_max_score = EXCLUDED.notify_max_score,
		       notify_sender = EXCLUDED.notify_sender, notify_subject = EXCLUDED.notify_subject,
		       notify_html_template = EXCLUDED.notify_html_template
		RETURNING `+quarantineSettingsColumns,
		s.TenantID, s.MaxSizeBytes, s.MaxAgeDays, s.RetentionSize, s.ExcludeDomains,
		s.Notify.Enabled, s.Notify.MaxScore.String(), s.Notify.Sender, s.Notify.Subject, s.Notify.HTMLTemplate))
	if err != nil {
		return fmt.Errorf("guardar ajustes de cuarentena: %w", err)
	}
	*s = *saved
	return nil
}
