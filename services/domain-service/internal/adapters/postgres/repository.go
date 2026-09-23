package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/domain-service/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Repository persiste en el esquema domains de la base de la empresa. El pool llega en
// el contexto (TenantPoolMiddleware en las peticiones, ForEachActiveTenant en el
// barrido); todas las consultas filtran ademas por tenant_id.
type Repository struct {
	pool *db.ContextPool
}

func NewRepository(pool *db.ContextPool) *Repository {
	return &Repository{pool: pool}
}

const domainColumns = `id, tenant_id, domain, purpose, status, verification_token, verified_at, last_checked_at,
 dkim_selector, dkim_private_key_enc, dkim_public_key, dkim_key_bits,
 dkim_previous_selector, dkim_previous_private_key_enc, dkim_previous_public_key, dkim_rotated_at,
 dkim_previous_signed_at, dkim_confirmed_at, dkim_revocation_pending,
 dmarc_policy, directory_deactivation_pending, dns_mode, dns_published_at,
 ses_identity_status, ses_dkim_status, ses_mail_from_status, ses_checked_at, ses_last_error, created_at, updated_at`

// dkimLockClass es el espacio de los cerrojos consultivos de claves DKIM en la base de la empresa;
// el segundo entero es el hash del id del dominio.
const dkimLockClass int32 = 0x646b696d // "dkim"

func scanDomain(row pgx.Row) (*domain.Domain, error) {
	d := &domain.Domain{}
	var prevSelector, prevPublic, sesIdentity, sesDKIM, sesMailFrom, sesError *string
	err := row.Scan(
		&d.ID, &d.TenantID, &d.Domain, &d.Purpose, &d.Status, &d.VerificationToken, &d.VerifiedAt, &d.LastCheckedAt,
		&d.DKIMSelector, &d.DKIMPrivateKeyEnc, &d.DKIMPublicKey, &d.DKIMKeyBits,
		&prevSelector, &d.DKIMPreviousPrivateKeyEnc, &prevPublic, &d.DKIMRotatedAt,
		&d.DKIMPreviousSignedAt, &d.DKIMConfirmedAt, &d.DKIMRevocationPending,
		&d.DMARCPolicy, &d.DirectoryDeactivationPending, &d.DNSMode, &d.DNSPublishedAt,
		&sesIdentity, &sesDKIM, &sesMailFrom, &d.SES.CheckedAt, &sesError, &d.CreatedAt, &d.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrDomainNotFound
		}
		return nil, err
	}
	if prevSelector != nil {
		d.DKIMPreviousSelector = *prevSelector
	}
	if prevPublic != nil {
		d.DKIMPreviousPublicKey = *prevPublic
	}
	d.SES.IdentityStatus = domain.SESIdentityStatus(deref(sesIdentity))
	d.SES.DKIMStatus = domain.SESCheckStatus(deref(sesDKIM))
	d.SES.MailFromStatus = domain.SESCheckStatus(deref(sesMailFrom))
	d.SES.LastError = deref(sesError)
	return d, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// isUniqueViolation reconoce el choque con domains_tenant_domain_key.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// schemaNotReady traduce un esquema o una columna que no existe (SQLSTATE 3F000, 42P01 y 42703) a
// domain.ErrTenantSchemaNotReady: en una empresa que se esta aprovisionando o a la que falta una migracion no
// es un fallo de codigo sino un estado que el barrido debe saltar.
func schemaNotReady(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "3F000", "42P01", "42703":
			return fmt.Errorf("%w: %s", domain.ErrTenantSchemaNotReady, pgErr.Message)
		}
	}
	return err
}

func (r *Repository) Create(ctx context.Context, d *domain.Domain) error {
	err := r.pool.QueryRow(ctx,
		`INSERT INTO domains.domains (id, tenant_id, domain, purpose, status, verification_token,
 dkim_selector, dkim_private_key_enc, dkim_public_key, dkim_key_bits, dmarc_policy)
 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING created_at, updated_at`,
		d.ID, d.TenantID, d.Domain, d.Purpose, d.Status, d.VerificationToken,
		d.DKIMSelector, d.DKIMPrivateKeyEnc, d.DKIMPublicKey, d.DKIMKeyBits, d.DMARCPolicy,
	).Scan(&d.CreatedAt, &d.UpdatedAt)
	if isUniqueViolation(err) {
		return domain.ErrDomainAlreadyExists
	}
	return err
}

func (r *Repository) GetByID(ctx context.Context, tenantID, id uuid.UUID) (*domain.Domain, error) {
	return scanDomain(r.pool.QueryRow(ctx,
		`SELECT `+domainColumns+` FROM domains.domains WHERE tenant_id = $1 AND id = $2`, tenantID, id))
}

func (r *Repository) GetByName(ctx context.Context, tenantID uuid.UUID, name string) (*domain.Domain, error) {
	return scanDomain(r.pool.QueryRow(ctx,
		`SELECT `+domainColumns+` FROM domains.domains WHERE tenant_id = $1 AND domain = $2`, tenantID, name))
}

func (r *Repository) List(ctx context.Context, tenantID uuid.UUID, offset, limit int) ([]*domain.Domain, int64, error) {
	var total int64
	if err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM domains.domains WHERE tenant_id = $1`, tenantID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.pool.Query(ctx,
		`SELECT `+domainColumns+` FROM domains.domains WHERE tenant_id = $1 ORDER BY domain LIMIT $2 OFFSET $3`,
		tenantID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	domains, err := collect(rows)
	return domains, total, err
}

func collect(rows pgx.Rows) ([]*domain.Domain, error) {
	var out []*domain.Domain
	for rows.Next() {
		d, err := scanDomain(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Update no escribe ninguna columna DKIM (ports.Repository): las claves cambian solo con
// SaveDKIMKeys y los metodos condicionados al selector.
func (r *Repository) Update(ctx context.Context, d *domain.Domain) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE domains.domains SET purpose = $3, status = $4, verified_at = $5, last_checked_at = $6,
 dmarc_policy = $7, directory_deactivation_pending = $8
 WHERE tenant_id = $1 AND id = $2`,
		d.TenantID, d.ID, d.Purpose, d.Status, d.VerifiedAt, d.LastCheckedAt,
		d.DMARCPolicy, d.DirectoryDeactivationPending,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrDomainNotFound
	}
	return nil
}

// Delete borra el dominio; las comprobaciones caen por ON DELETE CASCADE.
func (r *Repository) Delete(ctx context.Context, tenantID, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM domains.domains WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrDomainNotFound
	}
	return nil
}

func (r *Repository) ListForRecheck(ctx context.Context, tenantID uuid.UUID, pendingSince time.Time) ([]*domain.Domain, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+domainColumns+` FROM domains.domains
 WHERE tenant_id = $1 AND (status = $2 OR (status = $3 AND created_at >= $4))
 ORDER BY last_checked_at NULLS FIRST, domain`,
		tenantID, domain.StatusVerified, domain.StatusPending, pendingSince)
	if err != nil {
		return nil, schemaNotReady(err)
	}
	defer rows.Close()
	return collect(rows)
}

// ListWithExpiredPreviousDKIM mide la gracia desde la ultima vez que la clave anterior pudo
// firmar (domain.Domain.PreviousDKIMSigningEnd).
func (r *Repository) ListWithExpiredPreviousDKIM(ctx context.Context, tenantID uuid.UUID, rotatedBefore time.Time) ([]*domain.Domain, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+domainColumns+` FROM domains.domains
 WHERE tenant_id = $1 AND dkim_previous_selector IS NOT NULL
   AND GREATEST(dkim_rotated_at, COALESCE(dkim_previous_signed_at, dkim_rotated_at)) < $2
 ORDER BY dkim_rotated_at`,
		tenantID, rotatedBefore)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collect(rows)
}

func (r *Repository) ListPendingDeactivation(ctx context.Context, tenantID uuid.UUID) ([]*domain.Domain, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+domainColumns+` FROM domains.domains
 WHERE tenant_id = $1 AND directory_deactivation_pending
 ORDER BY domain`,
		tenantID)
	if err != nil {
		return nil, schemaNotReady(err)
	}
	defer rows.Close()
	return collect(rows)
}

func (r *Repository) ListPendingDKIMRevocation(ctx context.Context, tenantID uuid.UUID) ([]*domain.Domain, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+domainColumns+` FROM domains.domains
 WHERE tenant_id = $1 AND dkim_revocation_pending
 ORDER BY domain`,
		tenantID)
	if err != nil {
		return nil, schemaNotReady(err)
	}
	defer rows.Close()
	return collect(rows)
}

// ListPendingSESSync: los verificados se sincronizan con SES en cada reverificacion.
func (r *Repository) ListPendingSESSync(ctx context.Context, tenantID uuid.UUID) ([]*domain.Domain, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+domainColumns+` FROM domains.domains
 WHERE tenant_id = $1 AND ses_last_error IS NOT NULL AND status <> $2
 ORDER BY domain`,
		tenantID, domain.StatusVerified)
	if err != nil {
		return nil, schemaNotReady(err)
	}
	defer rows.Close()
	return collect(rows)
}

// SaveSESState escribe solo las columnas ses_*: el resto de la fila es de Update y de los metodos DKIM.
func (r *Repository) SaveSESState(ctx context.Context, d *domain.Domain) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE domains.domains SET ses_identity_status = $3, ses_dkim_status = $4, ses_mail_from_status = $5,
 ses_checked_at = $6, ses_last_error = $7
 WHERE tenant_id = $1 AND id = $2`,
		d.TenantID, d.ID, nullable(string(d.SES.IdentityStatus)), nullable(string(d.SES.DKIMStatus)),
		nullable(string(d.SES.MailFromStatus)), d.SES.CheckedAt, nullable(d.SES.LastError),
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrDomainNotFound
	}
	return nil
}

// WithDKIMLock toma pg_advisory_xact_lock sobre el dominio: se suelta al terminar la transaccion,
// asi que es seguro a traves de pgbouncer en modo transaccion. fn recibe la fila leida con el
// cerrojo tomado y todo lo que escribe va por la misma transaccion.
func (r *Repository) WithDKIMLock(ctx context.Context, tenantID, id uuid.UUID, fn func(ctx context.Context, d *domain.Domain) error) error {
	return r.pool.Transact(ctx, func(ctx context.Context) error {
		if _, err := r.pool.Exec(ctx, `SELECT pg_advisory_xact_lock($1, hashtext($2))`, dkimLockClass, id.String()); err != nil {
			return err
		}
		d, err := r.GetByID(ctx, tenantID, id)
		if err != nil {
			return err
		}
		return fn(ctx, d)
	})
}

// inTx corre fn en la transaccion del contexto o, si no la hay, en una propia.
func (r *Repository) inTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if db.HasTx(ctx) {
		return fn(ctx)
	}
	return r.pool.Transact(ctx, fn)
}

func (r *Repository) SaveDKIMKeys(ctx context.Context, d *domain.Domain, expectedSelector string, rotation *domain.DKIMRotation) error {
	return r.inTx(ctx, func(ctx context.Context) error {
		tag, err := r.pool.Exec(ctx,
			`UPDATE domains.domains SET dkim_selector = $4, dkim_private_key_enc = $5, dkim_public_key = $6, dkim_key_bits = $7,
 dkim_previous_selector = $8, dkim_previous_private_key_enc = $9, dkim_previous_public_key = $10, dkim_rotated_at = $11,
 dkim_previous_signed_at = $12, dkim_confirmed_at = $13, dkim_revocation_pending = $14
 WHERE tenant_id = $1 AND id = $2 AND dkim_selector = $3`,
			d.TenantID, d.ID, expectedSelector,
			d.DKIMSelector, d.DKIMPrivateKeyEnc, d.DKIMPublicKey, d.DKIMKeyBits,
			nullable(d.DKIMPreviousSelector), d.DKIMPreviousPrivateKeyEnc, nullable(d.DKIMPreviousPublicKey), d.DKIMRotatedAt,
			d.DKIMPreviousSignedAt, d.DKIMConfirmedAt, d.DKIMRevocationPending,
		)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			if _, err := r.GetByID(ctx, d.TenantID, d.ID); err != nil {
				return err
			}
			return domain.ErrDKIMKeysChanged
		}
		revoked := rotation.RevokedSelectors
		if revoked == nil {
			revoked = []string{}
		}
		var actor *uuid.UUID
		if rotation.ActorID != uuid.Nil {
			actor = &rotation.ActorID
		}
		if rotation.ID == uuid.Nil {
			rotation.ID = uuid.New()
		}
		_, err = r.pool.Exec(ctx,
			`INSERT INTO domains.dkim_rotations (id, tenant_id, domain_id, kind, selector, previous_selector, revoked_selectors, reason, actor_id, rotated_at)
 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			rotation.ID, rotation.TenantID, rotation.DomainID, rotation.Kind, rotation.Selector,
			nullable(rotation.PreviousSelector), revoked, rotation.Reason, actor, rotation.RotatedAt,
		)
		return err
	})
}

func (r *Repository) ClearPreviousDKIM(ctx context.Context, tenantID, id uuid.UUID, selector string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE domains.domains SET dkim_previous_selector = NULL, dkim_previous_private_key_enc = NULL,
 dkim_previous_public_key = NULL, dkim_rotated_at = NULL, dkim_previous_signed_at = NULL
 WHERE tenant_id = $1 AND id = $2 AND dkim_previous_selector = $3`,
		tenantID, id, selector)
	return err
}

func (r *Repository) MarkPreviousDKIMSigning(ctx context.Context, tenantID, id uuid.UUID, selector string, at time.Time) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE domains.domains SET dkim_previous_signed_at = GREATEST(COALESCE(dkim_previous_signed_at, $4), $4)
 WHERE tenant_id = $1 AND id = $2 AND dkim_previous_selector = $3`,
		tenantID, id, selector, at)
	return err
}

func (r *Repository) ConfirmDKIM(ctx context.Context, tenantID, id uuid.UUID, selector string, at time.Time) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE domains.domains SET dkim_confirmed_at = $4
 WHERE tenant_id = $1 AND id = $2 AND dkim_selector = $3 AND dkim_confirmed_at IS NULL`,
		tenantID, id, selector, at)
	return err
}

func (r *Repository) CompleteDKIMRevocation(ctx context.Context, tenantID, id uuid.UUID, selector string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE domains.domains SET dkim_revocation_pending = false
 WHERE tenant_id = $1 AND id = $2 AND dkim_selector = $3`,
		tenantID, id, selector)
	return err
}

func (r *Repository) ListDKIMRotations(ctx context.Context, tenantID, domainID uuid.UUID, limit int) ([]domain.DKIMRotation, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, tenant_id, domain_id, kind, selector, previous_selector, revoked_selectors, reason, actor_id, rotated_at
 FROM domains.dkim_rotations WHERE tenant_id = $1 AND domain_id = $2
 ORDER BY rotated_at DESC, id LIMIT $3`,
		tenantID, domainID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.DKIMRotation
	for rows.Next() {
		var rot domain.DKIMRotation
		var previous *string
		var actor *uuid.UUID
		if err := rows.Scan(&rot.ID, &rot.TenantID, &rot.DomainID, &rot.Kind, &rot.Selector, &previous,
			&rot.RevokedSelectors, &rot.Reason, &actor, &rot.RotatedAt); err != nil {
			return nil, err
		}
		if previous != nil {
			rot.PreviousSelector = *previous
		}
		if actor != nil {
			rot.ActorID = *actor
		}
		out = append(out, rot)
	}
	return out, rows.Err()
}

func (r *Repository) UsedDKIMSelectors(ctx context.Context, tenantID, domainID uuid.UUID) ([]string, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT selector FROM domains.dkim_rotations WHERE tenant_id = $1 AND domain_id = $2
 UNION SELECT previous_selector FROM domains.dkim_rotations
  WHERE tenant_id = $1 AND domain_id = $2 AND previous_selector IS NOT NULL
 UNION SELECT unnest(revoked_selectors) FROM domains.dkim_rotations WHERE tenant_id = $1 AND domain_id = $2`,
		tenantID, domainID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// SaveChecks inserta las comprobaciones de una verificacion en una sola transaccion:
// o queda la foto completa o no queda nada.
func (r *Repository) SaveChecks(ctx context.Context, checks []domain.DNSCheck) error {
	if len(checks) == 0 {
		return nil
	}
	return r.pool.Transact(ctx, func(ctx context.Context) error {
		for i := range checks {
			c := &checks[i]
			if c.ID == uuid.Nil {
				c.ID = uuid.New()
			}
			if _, err := r.pool.Exec(ctx,
				`INSERT INTO domains.dns_checks (id, tenant_id, domain_id, checked_at, record, expected, observed, ok, detail)
 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
				c.ID, c.TenantID, c.DomainID, c.CheckedAt, c.Record, c.Expected, c.Observed, c.OK, c.Detail,
			); err != nil {
				return err
			}
		}
		return nil
	})
}

// LatestChecks devuelve la comprobacion mas reciente de cada registro del dominio.
func (r *Repository) LatestChecks(ctx context.Context, tenantID, domainID uuid.UUID) ([]domain.DNSCheck, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT DISTINCT ON (record) id, tenant_id, domain_id, checked_at, record, expected, observed, ok, detail
 FROM domains.dns_checks WHERE tenant_id = $1 AND domain_id = $2
 ORDER BY record, checked_at DESC`,
		tenantID, domainID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.DNSCheck
	for rows.Next() {
		var c domain.DNSCheck
		if err := rows.Scan(&c.ID, &c.TenantID, &c.DomainID, &c.CheckedAt, &c.Record, &c.Expected, &c.Observed, &c.OK, &c.Detail); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *Repository) PruneChecks(ctx context.Context, tenantID uuid.UUID, before time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM domains.dns_checks WHERE tenant_id = $1 AND checked_at < $2`, tenantID, before)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
