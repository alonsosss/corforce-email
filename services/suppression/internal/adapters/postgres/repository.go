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

// uniqueViolation es el SQLSTATE de un choque con UNIQUE (tenant_id, email).
const uniqueViolation = "23505"

const entryColumns = `id, tenant_id, email, reason, source, detail, message_id, campaign_id, expires_at, created_at, updated_at`

// Repository implementa ports.EntryRepository sobre la base de la empresa. El pool o la
// transaccion salen del contexto (db.ContextPool).
type Repository struct {
	pool *db.ContextPool
}

func NewRepository(pool *db.ContextPool) *Repository {
	return &Repository{pool: pool}
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

func (r *Repository) GetByEmailForUpdate(ctx context.Context, tenantID uuid.UUID, email string) (*domain.Entry, error) {
	return scanEntry(r.pool.QueryRow(ctx,
		`SELECT `+entryColumns+` FROM suppression.entries WHERE tenant_id = $1 AND email = $2 FOR UPDATE`,
		tenantID, email))
}

func (r *Repository) FindByEmails(ctx context.Context, tenantID uuid.UUID, emails []string) ([]domain.Entry, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+entryColumns+` FROM suppression.entries WHERE tenant_id = $1 AND email = ANY($2::text[])`,
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

func (r *Repository) List(ctx context.Context, tenantID uuid.UUID, f ports.ListFilter) ([]domain.Entry, int64, error) {
	where := `WHERE tenant_id = $1 AND ($2 = '' OR reason = $2) AND ($3 = '' OR email LIKE '%' || $3 || '%')`
	args := []interface{}{tenantID, string(f.Reason), escapeLike(strings.ToLower(strings.TrimSpace(f.Search)))}

	var total int64
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM suppression.entries `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.pool.Query(ctx,
		`SELECT `+entryColumns+` FROM suppression.entries `+where+` ORDER BY created_at DESC, id LIMIT $4 OFFSET $5`,
		append(args, f.PerPage, (f.Page-1)*f.PerPage)...)
	if err != nil {
		return nil, 0, err
	}
	out, err := collectEntries(rows)
	if err != nil {
		return nil, 0, err
	}
	return out, total, nil
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

// InsertMissing inserta en una sola sentencia; ON CONFLICT DO NOTHING omite las que ya
// estaban (y las repetidas dentro del propio lote, que el caso de uso ya elimina).
func (r *Repository) InsertMissing(ctx context.Context, tenantID uuid.UUID, emails []string, reason domain.Reason, source, detail string) ([]domain.Entry, error) {
	rows, err := r.pool.Query(ctx,
		`INSERT INTO suppression.entries (tenant_id, email, reason, source, detail)
		 SELECT $1, e, $3, $4, $5 FROM unnest($2::text[]) AS e
		 ON CONFLICT (tenant_id, email) DO NOTHING
		 RETURNING `+entryColumns,
		tenantID, emails, reason, source, detail)
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
		`SELECT reason, count(*) FROM suppression.entries
		  WHERE tenant_id = $1 AND (expires_at IS NULL OR expires_at > $2)
		  GROUP BY reason`,
		tenantID, now)
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
