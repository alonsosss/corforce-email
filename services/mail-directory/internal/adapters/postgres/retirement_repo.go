package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// retirementLockSpace es la primera mitad de la clave del cerrojo de la baja de una empresa
// ("mdrt"). La forma de dos enteros no comparte espacio con los cerrojos de lider (un bigint) ni
// con los DKIM de mail-security ("dkim"), que viven en la misma base de la celda.
const retirementLockSpace int32 = 0x6d647274

// RetirementRepo implementa ports.RetirementRepository sobre mail.tenant_retirements (migracion
// 08) y las tablas del directorio. Cada sentencia filtra por tenant_id: la ruta interna de la baja
// corre sin usuario, sin el rol mail_app que acota por RLS.
type RetirementRepo struct {
	pool *db.ContextPool
}

func NewRetirementRepo(pool *db.ContextPool) *RetirementRepo { return &RetirementRepo{pool: pool} }

func (r *RetirementRepo) HoldShared(ctx context.Context, tenantID uuid.UUID) (bool, error) {
	if _, err := r.pool.Exec(ctx, `SELECT pg_advisory_xact_lock_shared($1, hashtext($2))`, retirementLockSpace, tenantID.String()); err != nil {
		return false, fmt.Errorf("cerrojo de la empresa %s: %w", tenantID, err)
	}
	// Una sentencia aparte, con su propia foto: ve la baja que se confirmo mientras esperaba.
	var retired bool
	if err := r.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM mail.tenant_retirements WHERE tenant_id = $1)`, tenantID,
	).Scan(&retired); err != nil {
		return false, fmt.Errorf("baja de la empresa %s: %w", tenantID, err)
	}
	return retired, nil
}

func (r *RetirementRepo) HoldExclusive(ctx context.Context, tenantID uuid.UUID) error {
	if _, err := r.pool.Exec(ctx, `SELECT pg_advisory_xact_lock($1, hashtext($2))`, retirementLockSpace, tenantID.String()); err != nil {
		return fmt.Errorf("cerrojo de la baja de la empresa %s: %w", tenantID, err)
	}
	return nil
}

func (r *RetirementRepo) Mark(ctx context.Context, tenantID uuid.UUID) (time.Time, error) {
	if _, err := r.pool.Exec(ctx,
		`INSERT INTO mail.tenant_retirements (tenant_id) VALUES ($1) ON CONFLICT (tenant_id) DO NOTHING`, tenantID,
	); err != nil {
		return time.Time{}, fmt.Errorf("registrar la baja de la empresa %s: %w", tenantID, err)
	}
	var retiredAt time.Time
	if err := r.pool.QueryRow(ctx,
		`SELECT retired_at FROM mail.tenant_retirements WHERE tenant_id = $1`, tenantID,
	).Scan(&retiredAt); err != nil {
		return time.Time{}, fmt.Errorf("baja de la empresa %s: %w", tenantID, err)
	}
	return retiredAt, nil
}

func (r *RetirementRepo) DeactivateDomains(ctx context.Context, tenantID uuid.UUID) ([]domain.Domain, error) {
	return collectRows(ctx, r.pool, scanDomain,
		`UPDATE mail.domains SET active = false WHERE tenant_id = $1 AND active RETURNING `+domainColumns, tenantID)
}

func (r *RetirementRepo) DeactivateAliasDomains(ctx context.Context, tenantID uuid.UUID) ([]domain.AliasDomain, error) {
	return collectRows(ctx, r.pool, scanAliasDomain,
		`UPDATE mail.alias_domains SET active = false WHERE tenant_id = $1 AND active RETURNING `+aliasDomainColumns, tenantID)
}

func (r *RetirementRepo) DeactivateMailboxes(ctx context.Context, tenantID uuid.UUID) ([]domain.Mailbox, error) {
	return collectRows(ctx, r.pool, scanMailbox,
		`UPDATE mail.mailboxes SET active = 0 WHERE tenant_id = $1 AND active <> 0 RETURNING `+mailboxColumns, tenantID)
}

func (r *RetirementRepo) DeactivateAliases(ctx context.Context, tenantID uuid.UUID) ([]domain.Alias, error) {
	return collectRows(ctx, r.pool, scanAlias,
		`UPDATE mail.aliases SET active = 0 WHERE tenant_id = $1 AND active <> 0 RETURNING `+aliasColumns, tenantID)
}

func (r *RetirementRepo) DeactivateSettings(ctx context.Context, tenantID uuid.UUID) (domain.RetirementCounts, error) {
	var c domain.RetirementCounts
	for _, s := range []struct {
		count *int
		sql   string
	}{
		{&c.AppPasswords, `UPDATE mail.app_passwords SET active = false WHERE tenant_id = $1 AND active`},
		{&c.Relayhosts, `UPDATE mail.relayhosts SET active = false, password = '' WHERE tenant_id = $1 AND (active OR password <> '')`},
		{&c.Transports, `UPDATE mail.transports SET active = false, password = '' WHERE tenant_id = $1 AND (active OR password <> '')`},
		{&c.TLSPolicies, `UPDATE mail.tls_policy_overrides SET active = false WHERE tenant_id = $1 AND active`},
		{&c.RecipientMaps, `UPDATE mail.recipient_maps SET active = false WHERE tenant_id = $1 AND active`},
		{&c.BCCMaps, `UPDATE mail.bcc_maps SET active = false WHERE tenant_id = $1 AND active`},
	} {
		tag, err := r.pool.Exec(ctx, s.sql, tenantID)
		if err != nil {
			return domain.RetirementCounts{}, fmt.Errorf("apagar el directorio de la empresa %s: %w", tenantID, err)
		}
		*s.count = int(tag.RowsAffected())
	}
	return c, nil
}

// collectRows ejecuta una sentencia que devuelve filas del directorio y las lee todas.
func collectRows[T any](ctx context.Context, pool *db.ContextPool, scan func(pgx.Row) (T, error), sql string, args ...interface{}) ([]T, error) {
	rows, err := pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		item, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
