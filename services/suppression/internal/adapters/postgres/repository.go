package postgres

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/suppression/internal/domain"
	"github.com/alonsosss/corforce-email/services/suppression/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// uniqueViolation es el SQLSTATE de un choque con UNIQUE (tenant_id, email, reason).
const uniqueViolation = "23505"

const entryColumns = `id, tenant_id, email, reason, source, detail, message_id, campaign_id, expires_at, created_at, updated_at`

// addressLockPrefix separa las claves de bloqueo de este servicio de cualquier otro
// bloqueo consultivo que se tome en la misma base de empresa.
const addressLockPrefix = "suppression:address:"

// Repository implementa ports.EntryRepository sobre la base de la empresa. El pool o la
// transaccion salen del contexto (db.ContextPool).
type Repository struct {
	pool *db.ContextPool
}

func NewRepository(pool *db.ContextPool) *Repository {
	return &Repository{pool: pool}
}

// severityOrder es domain.Reasons() como texto. Las consultas eligen la causa principal
// de una direccion por la posicion en esta lista, asi que el orden de gravedad vive solo
// en el dominio.
func severityOrder() []string {
	reasons := domain.Reasons()
	out := make([]string, len(reasons))
	for i, r := range reasons {
		out[i] = string(r)
	}
	return out
}

func scanEntry(row pgx.Row) (*domain.Entry, error) {
	var e domain.Entry
	if err := row.Scan(&e.ID, &e.TenantID, &e.Email, &e.Reason, &e.Source, &e.Detail,
		&e.MessageID, &e.CampaignID, &e.ExpiresAt, &e.CreatedAt, &e.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrEntryNotFound
		}
		return nil, err
	}
	return &e, nil
}

func collectEntries(rows pgx.Rows) ([]domain.Entry, error) {
	defer rows.Close()
	var out []domain.Entry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Repository) GetByID(ctx context.Context, tenantID, id uuid.UUID) (*domain.Entry, error) {
	return scanEntry(r.pool.QueryRow(ctx,
		`SELECT `+entryColumns+` FROM suppression.entries WHERE tenant_id = $1 AND id = $2`,
		tenantID, id))
}

func (r *Repository) LockAddress(ctx context.Context, tenantID uuid.UUID, email string) error {
	_, err := r.pool.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		addressLockPrefix+tenantID.String()+":"+email)
	return err
}

func (r *Repository) GetCauseForUpdate(ctx context.Context, tenantID uuid.UUID, email string, reason domain.Reason) (*domain.Entry, error) {
	return scanEntry(r.pool.QueryRow(ctx,
		`SELECT `+entryColumns+` FROM suppression.entries
		  WHERE tenant_id = $1 AND email = $2 AND reason = $3 FOR UPDATE`,
		tenantID, email, reason))
}

func (r *Repository) FindByEmails(ctx context.Context, tenantID uuid.UUID, emails []string) ([]domain.Entry, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+entryColumns+` FROM suppression.entries
		  WHERE tenant_id = $1 AND email = ANY($2::text[])
		  ORDER BY email, created_at, id`,
		tenantID, emails)
	if err != nil {
		return nil, err
	}
	return collectEntries(rows)
}

// escapeLike neutraliza los comodines de LIKE en la busqueda del usuario: buscar "%"
// debe encontrar direcciones con ese caracter, no todas.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// principalCTE elige la causa principal de cada direccion de la empresa: primero las
// vigentes y, entre ellas, la mas grave. Parametros: $1 empresa, $3 busqueda, $4 ahora,
// $5 orden de gravedad.
const principalCTE = `WITH principal AS (
	SELECT DISTINCT ON (email) email, reason, created_at, id
	  FROM suppression.entries
	 WHERE tenant_id = $1 AND ($3 = '' OR email LIKE '%' || $3 || '%')
	 ORDER BY email, (expires_at IS NULL OR expires_at > $4) DESC, array_position($5::text[], reason::text), id
)`

func (r *Repository) ListAddresses(ctx context.Context, tenantID uuid.UUID, f ports.ListFilter, now time.Time) ([]string, int64, error) {
	where := ` WHERE ($2 = '' OR reason = $2)`
	args := []interface{}{tenantID, string(f.Reason), escapeLike(strings.ToLower(strings.TrimSpace(f.Search))), now, severityOrder()}

	var total int64
	if err := r.pool.QueryRow(ctx, principalCTE+` SELECT count(*) FROM principal`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.pool.Query(ctx,
		principalCTE+` SELECT email FROM principal`+where+` ORDER BY created_at DESC, id LIMIT $6 OFFSET $7`,
		append(args, f.PerPage, (f.Page-1)*f.PerPage)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var emails []string
	for rows.Next() {
		var email string
		if err := rows.Scan(&email); err != nil {
			return nil, 0, err
		}
		emails = append(emails, email)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return emails, total, nil
}

func (r *Repository) Insert(ctx context.Context, e *domain.Entry) error {
	err := r.pool.QueryRow(ctx,
		`INSERT INTO suppression.entries (tenant_id, email, reason, source, detail, message_id, campaign_id, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 RETURNING id, created_at, updated_at`,
		e.TenantID, e.Email, e.Reason, e.Source, e.Detail, e.MessageID, e.CampaignID, e.ExpiresAt,
	).Scan(&e.ID, &e.CreatedAt, &e.UpdatedAt)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return domain.ErrEntryAlreadyExists
	}
	return err
}

// InsertMissing inserta en una sola sentencia. ON CONFLICT solo actua sobre una causa
// caducada (la reactiva sin caducidad); una vigente no cambia y no se devuelve. DISTINCT
// protege de una direccion repetida en el lote, que haria fallar el DO UPDATE.
func (r *Repository) InsertMissing(ctx context.Context, tenantID uuid.UUID, emails []string, reason domain.Reason, source, detail string, now time.Time) ([]domain.Entry, error) {
	rows, err := r.pool.Query(ctx,
		`INSERT INTO suppression.entries AS e (tenant_id, email, reason, source, detail)
		 SELECT $1, x.email, $3, $4, $5 FROM (SELECT DISTINCT unnest($2::text[]) AS email) AS x
		 ON CONFLICT (tenant_id, email, reason) DO UPDATE
		    SET source = EXCLUDED.source, detail = EXCLUDED.detail,
		        message_id = NULL, campaign_id = NULL, expires_at = NULL
		  WHERE e.expires_at IS NOT NULL AND e.expires_at <= $6
		 RETURNING `+entryColumns,
		tenantID, emails, reason, source, detail, now)
	if err != nil {
		return nil, err
	}
	return collectEntries(rows)
}

func (r *Repository) Update(ctx context.Context, e *domain.Entry) error {
	err := r.pool.QueryRow(ctx,
		`UPDATE suppression.entries
		    SET reason = $3, source = $4, detail = $5, message_id = $6, campaign_id = $7, expires_at = $8
		  WHERE tenant_id = $1 AND id = $2
		  RETURNING updated_at`,
		e.TenantID, e.ID, e.Reason, e.Source, e.Detail, e.MessageID, e.CampaignID, e.ExpiresAt,
	).Scan(&e.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrEntryNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return domain.ErrEntryAlreadyExists
	}
	return err
}

// Reregister fecha la causa con clock_timestamp(), la hora de esta sentencia y no la del
// inicio de la transaccion (now()), que puede ser anterior a un consentimiento confirmado
// mientras la transaccion esperaba el bloqueo de la direccion. updated_at lo pone el
// trigger comun con now(), asi que tras volver a registrar puede quedar por detras de
// created_at; la hora que se compara es created_at.
func (r *Repository) Reregister(ctx context.Context, e *domain.Entry) error {
	err := r.pool.QueryRow(ctx,
		`UPDATE suppression.entries
		    SET source = $3, detail = $4, message_id = $5, campaign_id = $6, expires_at = $7,
		        created_at = clock_timestamp()
		  WHERE tenant_id = $1 AND id = $2
		  RETURNING created_at, updated_at`,
		e.TenantID, e.ID, e.Source, e.Detail, e.MessageID, e.CampaignID, e.ExpiresAt,
	).Scan(&e.CreatedAt, &e.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrEntryNotFound
	}
	return err
}

func (r *Repository) Delete(ctx context.Context, tenantID, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM suppression.entries WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrEntryNotFound
	}
	return nil
}

func (r *Repository) CountByReason(ctx context.Context, tenantID uuid.UUID, now time.Time) ([]domain.ReasonCount, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT reason, count(*) FROM (
		     SELECT DISTINCT ON (email) email, reason FROM suppression.entries
		      WHERE tenant_id = $1 AND (expires_at IS NULL OR expires_at > $2)
		      ORDER BY email, array_position($3::text[], reason::text)
		 ) AS principal
		 GROUP BY reason`,
		tenantID, now, severityOrder())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ReasonCount
	for rows.Next() {
		var c domain.ReasonCount
		if err := rows.Scan(&c.Reason, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
