package postgres

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/google/uuid"
)

// PolicyReader implementa ports.PolicyReader: lecturas a lo ancho de la celda para los
// endpoints de los motores y la reconciliacion de Redis. Corren como duena del pool
// porque no hay empresa en la peticion; solo leen y nunca devuelven credenciales.
type PolicyReader struct {
	pool *db.ContextPool
}

func NewPolicyReader(pool *db.ContextPool) *PolicyReader { return &PolicyReader{pool: pool} }

func (r *PolicyReader) AllSpamScores(ctx context.Context) ([]domain.SpamScore, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+spamScoreColumns+` FROM mail_security.spam_scores ORDER BY object`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.SpamScore
	for rows.Next() {
		s, err := scanSpamScore(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

func (r *PolicyReader) AllAddressLists(ctx context.Context) ([]domain.AddressListEntry, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+addressListColumns+` FROM mail_security.address_lists ORDER BY object, kind, pattern`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.AddressListEntry
	for rows.Next() {
		var e domain.AddressListEntry
		if err := rows.Scan(&e.ID, &e.TenantID, &e.Object, &e.Kind, &e.Pattern, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r *PolicyReader) AllActiveSettingsMaps(ctx context.Context) ([]domain.SettingsMap, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+settingsMapColumns+` FROM mail_security.settings_maps WHERE active ORDER BY tenant_id, created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.SettingsMap
	for rows.Next() {
		m, err := scanSettingsMap(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// PolicyUpdatedAt es el ultimo cambio de las tablas que alimentan /settings. Solo sirve
// para sembrar Last-Modified tras un reinicio: los borrados los detecta el hash del
// documento, no esta marca.
func (r *PolicyReader) PolicyUpdatedAt(ctx context.Context) (time.Time, error) {
	var t *time.Time
	err := r.pool.QueryRow(ctx, `
		SELECT GREATEST(
			(SELECT max(updated_at) FROM mail_security.spam_scores),
			(SELECT max(updated_at) FROM mail_security.address_lists),
			(SELECT max(updated_at) FROM mail_security.settings_maps))`).Scan(&t)
	if err != nil || t == nil {
		return time.Time{}, err
	}
	return *t, nil
}

func (r *PolicyReader) FooterByDomain(ctx context.Context, domainName string) (*domain.DomainFooter, error) {
	f, err := scanFooter(r.pool.QueryRow(ctx, `SELECT `+footerColumns+` FROM mail_security.domain_footers WHERE domain = $1`, domainName))
	return f, notFound(err)
}

func (r *PolicyReader) AllForwardingHosts(ctx context.Context) ([]domain.ForwardingHost, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+forwardingHostColumns+` FROM mail_security.forwarding_hosts ORDER BY host`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ForwardingHost
	for rows.Next() {
		h, err := scanForwardingHost(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *h)
	}
	return out, rows.Err()
}

func (r *PolicyReader) AllRateLimits(ctx context.Context) ([]domain.RateLimit, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+rateLimitColumns+` FROM mail_security.rate_limits ORDER BY object`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.RateLimit
	for rows.Next() {
		rl, err := scanRateLimit(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *rl)
	}
	return out, rows.Err()
}

func (r *PolicyReader) AllMailboxTags(ctx context.Context) ([]domain.MailboxTags, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+mailboxTagsColumns+` FROM mail_security.mailbox_tags ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.MailboxTags
	for rows.Next() {
		t, err := scanMailboxTags(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func (r *PolicyReader) AllQuarantineSettings(ctx context.Context) ([]domain.QuarantineSettings, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+quarantineSettingsColumns+` FROM mail_security.quarantine_settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.QuarantineSettings
	for rows.Next() {
		s, err := scanQuarantineSettings(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

func (r *PolicyReader) QuarantineSettingsFor(ctx context.Context, tenantID uuid.UUID) (*domain.QuarantineSettings, error) {
	s, err := scanQuarantineSettings(r.pool.QueryRow(ctx, `SELECT `+quarantineSettingsColumns+` FROM mail_security.quarantine_settings WHERE tenant_id = $1`, tenantID))
	return s, notFound(err)
}

// DeleteMailboxTagsByUsername limpia la fila cuando el directorio da de baja el buzon.
func (r *PolicyReader) DeleteMailboxTagsByUsername(ctx context.Context, username string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM mail_security.mailbox_tags WHERE username = $1`, username)
	return err
}
