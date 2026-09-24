// Package postgres guarda los metadatos de los ficheros compartidos en mail_files.shared_files, en la
// base de cada empresa. Toda consulta corre en una transaccion: es la que fija la empresa de la
// sesion (app.current_tenant_id) que exige la politica de fila del rol de servicio.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/mail-files/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Repository struct {
	pool *db.ContextPool
}

func NewRepository(pool *db.ContextPool) *Repository { return &Repository{pool: pool} }

const columns = `id, tenant_id, mailbox_id, file_name, size_bytes, sha256, object_key, status, expires_at,
	max_downloads, downloads, last_download_at, revoked_at, object_deleted_at IS NOT NULL, created_at`

// vigente es lo que ocupa cuota: subiendo o descargable, sin caducar ni agotar.
const vigente = `object_deleted_at IS NULL AND status IN ('pending', 'ready') AND expires_at > $3 AND downloads < max_downloads`

func scanFile(row pgx.Row) (domain.File, error) {
	var f domain.File
	var status string
	err := row.Scan(&f.ID, &f.TenantID, &f.MailboxID, &f.Name, &f.SizeBytes, &f.SHA256, &f.ObjectKey, &status,
		&f.ExpiresAt, &f.MaxDownloads, &f.Downloads, &f.LastDownloadAt, &f.RevokedAt, &f.ObjectDeleted, &f.CreatedAt)
	f.Status = domain.Status(status)
	return f, err
}

func unavailable(op string, err error) error {
	return fmt.Errorf("%w: %s: %v", domain.ErrUnavailable, op, err)
}

func (r *Repository) tx(ctx context.Context, op string, fn func(ctx context.Context) error) error {
	err := r.pool.Transact(ctx, fn)
	if err == nil {
		return nil
	}
	if isDomain(err) {
		return err
	}
	return unavailable(op, err)
}

// isDomain distingue un rechazo de negocio (se entrega tal cual) de un fallo de la base.
func isDomain(err error) bool {
	for _, target := range []error{
		domain.ErrNotFound, domain.ErrLinkInvalid, domain.ErrNotRevocable, domain.ErrMailboxQuota,
		domain.ErrTenantQuota, domain.ErrTooManyFiles, domain.ErrUnavailable,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

func (r *Repository) usage(ctx context.Context, owner domain.Owner, now time.Time) (domain.Usage, error) {
	var u domain.Usage
	err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(sum(size_bytes) FILTER (WHERE mailbox_id = $2), 0),
		       count(*) FILTER (WHERE mailbox_id = $2),
		       COALESCE(sum(size_bytes), 0)
		  FROM mail_files.shared_files
		 WHERE tenant_id = $1 AND `+vigente,
		owner.TenantID, owner.MailboxID, now).Scan(&u.MailboxBytes, &u.MailboxActive, &u.TenantBytes)
	return u, err
}

// CreatePending comprueba la cuota y reserva el fichero bajo un cerrojo por empresa: dos subidas
// simultaneas del mismo buzon o de la misma empresa no pueden pasar las dos una cuota que solo admite
// una.
func (r *Repository) CreatePending(ctx context.Context, f domain.File, policy domain.Policy, now time.Time) error {
	return r.tx(ctx, "reservar fichero", func(ctx context.Context) error {
		if _, err := r.pool.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('mail_files:' || $1::text, 0))`, f.TenantID); err != nil {
			return err
		}
		usage, err := r.usage(ctx, domain.Owner{TenantID: f.TenantID, MailboxID: f.MailboxID}, now)
		if err != nil {
			return err
		}
		if err := policy.Admits(usage, f.SizeBytes); err != nil {
			return err
		}
		_, err = r.pool.Exec(ctx, `
			INSERT INTO mail_files.shared_files
			       (id, tenant_id, mailbox_id, file_name, size_bytes, sha256, object_key, status, expires_at, max_downloads, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, 'pending', $8, $9, $10)`,
			f.ID, f.TenantID, f.MailboxID, f.Name, f.SizeBytes, f.SHA256, f.ObjectKey, f.ExpiresAt, f.MaxDownloads, now)
		return err
	})
}

func (r *Repository) MarkReady(ctx context.Context, tenantID, id uuid.UUID) (domain.File, error) {
	var f domain.File
	err := r.tx(ctx, "marcar listo", func(ctx context.Context) error {
		var err error
		f, err = scanFile(r.pool.QueryRow(ctx, `
			UPDATE mail_files.shared_files SET status = 'ready'
			 WHERE tenant_id = $1 AND id = $2 AND status = 'pending'
			RETURNING `+columns, tenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			// Revocado o dado por fallido mientras subia: el barrido borra lo que llego al almacen.
			return fmt.Errorf("%w: el fichero dejó de estar pendiente durante la subida", domain.ErrUnavailable)
		}
		return err
	})
	return f, err
}

func (r *Repository) MarkFailed(ctx context.Context, tenantID, id uuid.UUID) error {
	return r.tx(ctx, "marcar fallido", func(ctx context.Context) error {
		_, err := r.pool.Exec(ctx, `
			UPDATE mail_files.shared_files SET status = 'failed'
			 WHERE tenant_id = $1 AND id = $2 AND status = 'pending'`, tenantID, id)
		return err
	})
}

func (r *Repository) ListByMailbox(ctx context.Context, owner domain.Owner, limit int) ([]domain.File, error) {
	var out []domain.File
	err := r.tx(ctx, "listar", func(ctx context.Context) error {
		rows, err := r.pool.Query(ctx, `
			SELECT `+columns+`
			  FROM mail_files.shared_files
			 WHERE tenant_id = $1 AND mailbox_id = $2
			 ORDER BY created_at DESC, id
			 LIMIT $3`, owner.TenantID, owner.MailboxID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			f, err := scanFile(rows)
			if err != nil {
				return err
			}
			out = append(out, f)
		}
		return rows.Err()
	})
	return out, err
}

func (r *Repository) Usage(ctx context.Context, owner domain.Owner, now time.Time) (domain.Usage, error) {
	var u domain.Usage
	err := r.tx(ctx, "uso", func(ctx context.Context) error {
		var err error
		u, err = r.usage(ctx, owner, now)
		return err
	})
	return u, err
}

func (r *Repository) Revoke(ctx context.Context, owner domain.Owner, id uuid.UUID, now time.Time) (domain.File, error) {
	var f domain.File
	err := r.tx(ctx, "revocar", func(ctx context.Context) error {
		var err error
		f, err = scanFile(r.pool.QueryRow(ctx, `
			UPDATE mail_files.shared_files SET status = 'revoked', revoked_at = $4
			 WHERE tenant_id = $1 AND mailbox_id = $2 AND id = $3 AND status IN ('pending', 'ready')
			RETURNING `+columns, owner.TenantID, owner.MailboxID, id, now))
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var exists bool
		if err := r.pool.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM mail_files.shared_files WHERE tenant_id = $1 AND mailbox_id = $2 AND id = $3)`,
			owner.TenantID, owner.MailboxID, id).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return domain.ErrNotRevocable
		}
		return domain.ErrNotFound
	})
	return f, err
}

func (r *Repository) Get(ctx context.Context, tenantID, id uuid.UUID) (domain.File, error) {
	var f domain.File
	err := r.tx(ctx, "leer", func(ctx context.Context) error {
		var err error
		f, err = scanFile(r.pool.QueryRow(ctx, `
			SELECT `+columns+` FROM mail_files.shared_files WHERE tenant_id = $1 AND id = $2`, tenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		return err
	})
	return f, err
}

func (r *Repository) ClaimDownload(ctx context.Context, tenantID, id uuid.UUID, now time.Time) (domain.File, error) {
	var f domain.File
	err := r.tx(ctx, "contar descarga", func(ctx context.Context) error {
		var err error
		f, err = scanFile(r.pool.QueryRow(ctx, `
			UPDATE mail_files.shared_files SET downloads = downloads + 1, last_download_at = $3
			 WHERE tenant_id = $1 AND id = $2 AND status = 'ready' AND object_deleted_at IS NULL
			   AND expires_at > $3 AND downloads < max_downloads
			RETURNING `+columns, tenantID, id, now))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrLinkInvalid
		}
		return err
	})
	return f, err
}

func (r *Repository) MarkObjectDeleted(ctx context.Context, tenantID, id uuid.UUID) error {
	return r.tx(ctx, "marcar objeto borrado", func(ctx context.Context) error {
		_, err := r.pool.Exec(ctx, `
			UPDATE mail_files.shared_files SET object_deleted_at = now()
			 WHERE tenant_id = $1 AND id = $2 AND object_deleted_at IS NULL`, tenantID, id)
		return err
	})
}

func (r *Repository) ExpireDue(ctx context.Context, tenantID uuid.UUID, now time.Time) (int, error) {
	var n int64
	err := r.tx(ctx, "caducar", func(ctx context.Context) error {
		tag, err := r.pool.Exec(ctx, `
			UPDATE mail_files.shared_files SET status = 'expired'
			 WHERE tenant_id = $1 AND status = 'ready' AND expires_at <= $2`, tenantID, now)
		n = tag.RowsAffected()
		return err
	})
	return int(n), err
}

func (r *Repository) FailStalePending(ctx context.Context, tenantID uuid.UUID, before time.Time) (int, error) {
	var n int64
	err := r.tx(ctx, "cerrar subidas abandonadas", func(ctx context.Context) error {
		tag, err := r.pool.Exec(ctx, `
			UPDATE mail_files.shared_files SET status = 'failed'
			 WHERE tenant_id = $1 AND status = 'pending' AND created_at < $2`, tenantID, before)
		n = tag.RowsAffected()
		return err
	})
	return int(n), err
}

func (r *Repository) DueForDeletion(ctx context.Context, tenantID uuid.UUID, now time.Time, grace time.Duration, limit int) ([]domain.File, error) {
	var out []domain.File
	settled := now.Add(-grace)
	err := r.tx(ctx, "objetos a borrar", func(ctx context.Context) error {
		rows, err := r.pool.Query(ctx, `
			SELECT `+columns+`
			  FROM mail_files.shared_files
			 WHERE tenant_id = $1 AND object_deleted_at IS NULL
			   AND (status IN ('revoked', 'failed')
			        OR (status = 'expired' AND expires_at <= $2)
			        OR (status = 'ready' AND downloads >= max_downloads AND last_download_at <= $2))
			 ORDER BY updated_at, id
			 LIMIT $3`, tenantID, settled, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			f, err := scanFile(rows)
			if err != nil {
				return err
			}
			out = append(out, f)
		}
		return rows.Err()
	})
	return out, err
}

func (r *Repository) PurgeHistory(ctx context.Context, tenantID uuid.UUID, before time.Time) (int, error) {
	var n int64
	err := r.tx(ctx, "podar historial", func(ctx context.Context) error {
		tag, err := r.pool.Exec(ctx, `
			DELETE FROM mail_files.shared_files
			 WHERE tenant_id = $1 AND object_deleted_at IS NOT NULL AND object_deleted_at < $2`, tenantID, before)
		n = tag.RowsAffected()
		return err
	})
	return int(n), err
}
