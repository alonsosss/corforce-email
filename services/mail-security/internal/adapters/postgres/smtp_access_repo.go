package postgres

import (
	"context"
	"net/netip"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const smtpAccessColumns = `tenant_id, username, network, updated_at`

// groupSMTPAccess agrupa por buzon filas de smtpAccessColumns ordenadas por username.
func groupSMTPAccess(rows pgx.Rows, err error) ([]domain.SMTPAccess, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.SMTPAccess{}
	for rows.Next() {
		var tenantID uuid.UUID
		var username string
		var network netip.Prefix
		var updated time.Time
		if err := rows.Scan(&tenantID, &username, &network, &updated); err != nil {
			return nil, err
		}
		if n := len(out); n == 0 || out[n-1].Username != username {
			out = append(out, domain.SMTPAccess{TenantID: tenantID, Username: username, Networks: []string{}})
		}
		last := &out[len(out)-1]
		last.Networks = append(last.Networks, domain.SMTPNetworkField(network))
		if updated.After(last.UpdatedAt) {
			last.UpdatedAt = updated
		}
	}
	return out, rows.Err()
}

func (r *PolicyRepository) ListSMTPAccess(ctx context.Context, tenantID uuid.UUID) ([]domain.SMTPAccess, error) {
	return groupSMTPAccess(r.pool.Query(ctx,
		`SELECT `+smtpAccessColumns+` FROM mail_security.smtp_access_networks WHERE tenant_id = $1 ORDER BY username, network`, tenantID))
}

func (r *PolicyRepository) GetSMTPAccess(ctx context.Context, tenantID uuid.UUID, username string) (*domain.SMTPAccess, error) {
	all, err := groupSMTPAccess(r.pool.Query(ctx,
		`SELECT `+smtpAccessColumns+` FROM mail_security.smtp_access_networks WHERE tenant_id = $1 AND username = $2 ORDER BY network`,
		tenantID, username))
	if err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return nil, domain.ErrNotFound
	}
	return &all[0], nil
}

// ReplaceSMTPAccess borra e inserta en la transaccion del caso de uso: Rspamd nunca ve un
// estado intermedio porque Redis se escribe despues de confirmar.
func (r *PolicyRepository) ReplaceSMTPAccess(ctx context.Context, tenantID uuid.UUID, username string, networks []netip.Prefix) error {
	if _, err := r.pool.Exec(ctx,
		`DELETE FROM mail_security.smtp_access_networks WHERE tenant_id = $1 AND username = $2`, tenantID, username); err != nil {
		return err
	}
	for _, n := range networks {
		if _, err := r.pool.Exec(ctx,
			`INSERT INTO mail_security.smtp_access_networks (tenant_id, username, network) VALUES ($1, $2, $3)`,
			tenantID, username, n); err != nil {
			return err
		}
	}
	return nil
}

func (r *PolicyRepository) DeleteSMTPAccess(ctx context.Context, tenantID uuid.UUID, username string) error {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM mail_security.smtp_access_networks WHERE tenant_id = $1 AND username = $2`, tenantID, username)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *PolicyReader) AllSMTPAccess(ctx context.Context) ([]domain.SMTPAccess, error) {
	return groupSMTPAccess(r.pool.Query(ctx,
		`SELECT `+smtpAccessColumns+` FROM mail_security.smtp_access_networks ORDER BY username, network`))
}

// DeleteSMTPAccessByUsername limpia las redes cuando el directorio da de baja el buzon.
func (r *PolicyReader) DeleteSMTPAccessByUsername(ctx context.Context, username string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM mail_security.smtp_access_networks WHERE username = $1`, username)
	return err
}
