package postgres

import (
	"context"
	"errors"
	"sort"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/mail-dav/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Repository persiste en el esquema mail_dav de la base de la empresa. El pool llega en el contexto
// (lo deja tenantdb.Binder) y toda operacion corre en una transaccion cuya sesion lleva la empresa y el
// buzon del Principal, que es lo que leen las politicas de fila; ademas cada consulta filtra por las dos
// columnas, de modo que el aislamiento no depende solo de la politica.
type Repository struct {
	pool *db.ContextPool
}

func NewRepository(pool *db.ContextPool) *Repository { return &Repository{pool: pool} }

// mailboxLockClass es el espacio de los cerrojos consultivos por buzon: serializa el conteo de los
// limites (libretas y contactos) y el alta que lo sigue.
const mailboxLockClass int32 = 0x64617662 // "davb"

const uniqueViolation = "23505"

func (r *Repository) scoped(ctx context.Context, p domain.Principal, fn func(ctx context.Context) error) error {
	ctx = middleware.WithIdentity(ctx, p.MailboxID.String(), p.TenantID.String())
	return r.pool.Transact(ctx, fn)
}

func (r *Repository) lockMailbox(ctx context.Context, p domain.Principal) error {
	_, err := r.pool.Exec(ctx, `SELECT pg_advisory_xact_lock($1, hashtext($2))`, mailboxLockClass, p.MailboxID.String())
	return err
}

const bookColumns = `id, tenant_id, mailbox_id, slug, display_name, description, sync_seq, changes_floor, created_at, updated_at`

func scanBook(row pgx.Row) (domain.Addressbook, error) {
	var b domain.Addressbook
	err := row.Scan(&b.ID, &b.TenantID, &b.MailboxID, &b.Slug, &b.DisplayName, &b.Description, &b.SyncSeq, &b.ChangesFloor, &b.CreatedAt, &b.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return b, domain.ErrNotFound
	}
	return b, err
}

const contactColumns = `id, tenant_id, mailbox_id, addressbook_id, resource_name, uid, vcard, etag, display_name, emails, created_at, updated_at`

func scanContact(row pgx.Row) (domain.Contact, error) {
	var c domain.Contact
	err := row.Scan(&c.ID, &c.TenantID, &c.MailboxID, &c.AddressbookID, &c.ResourceName, &c.UID, &c.VCard, &c.ETag, &c.DisplayName, &c.Emails, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, domain.ErrNotFound
	}
	return c, err
}

func (r *Repository) book(ctx context.Context, p domain.Principal, slug string, forUpdate bool) (domain.Addressbook, error) {
	q := `SELECT ` + bookColumns + ` FROM mail_dav.addressbooks WHERE tenant_id = $1 AND mailbox_id = $2 AND slug = $3`
	if forUpdate {
		q += ` FOR UPDATE`
	}
	return scanBook(r.pool.QueryRow(ctx, q, p.TenantID, p.MailboxID, slug))
}

func (r *Repository) contacts(ctx context.Context, p domain.Principal, book domain.Addressbook, names []string) ([]domain.Contact, error) {
	q := `SELECT ` + contactColumns + ` FROM mail_dav.contacts WHERE tenant_id = $1 AND mailbox_id = $2 AND addressbook_id = $3`
	args := []any{p.TenantID, p.MailboxID, book.ID}
	if names != nil {
		q += ` AND resource_name = ANY($4)`
		args = append(args, names)
	}
	rows, err := r.pool.Query(ctx, q+` ORDER BY resource_name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Contact{}
	for rows.Next() {
		c, err := scanContact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *Repository) ListAddressbooks(ctx context.Context, p domain.Principal) ([]domain.Addressbook, error) {
	out := []domain.Addressbook{}
	err := r.scoped(ctx, p, func(ctx context.Context) error {
		rows, err := r.pool.Query(ctx, `SELECT `+bookColumns+` FROM mail_dav.addressbooks WHERE tenant_id = $1 AND mailbox_id = $2 ORDER BY slug`, p.TenantID, p.MailboxID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			b, err := scanBook(rows)
			if err != nil {
				return err
			}
			out = append(out, b)
		}
		return rows.Err()
	})
	return out, err
}

func (r *Repository) GetAddressbook(ctx context.Context, p domain.Principal, slug string) (domain.Addressbook, error) {
	var out domain.Addressbook
	err := r.scoped(ctx, p, func(ctx context.Context) (err error) {
		out, err = r.book(ctx, p, slug, false)
		return err
	})
	return out, err
}

func (r *Repository) CreateAddressbook(ctx context.Context, p domain.Principal, book domain.Addressbook, maxBooks int) (domain.Addressbook, error) {
	err := r.scoped(ctx, p, func(ctx context.Context) error {
		if err := r.lockMailbox(ctx, p); err != nil {
			return err
		}
		if _, err := r.book(ctx, p, book.Slug, false); err == nil {
			return domain.ErrAlreadyExists
		} else if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		var count int
		if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM mail_dav.addressbooks WHERE tenant_id = $1 AND mailbox_id = $2`, p.TenantID, p.MailboxID).Scan(&count); err != nil {
			return err
		}
		if count >= maxBooks {
			return domain.ErrAddressbookLimit
		}
		var err error
		book, err = scanBook(r.pool.QueryRow(ctx,
			`INSERT INTO mail_dav.addressbooks (id, tenant_id, mailbox_id, slug, display_name, description)
			 VALUES ($1, $2, $3, $4, $5, $6) RETURNING `+bookColumns,
			book.ID, p.TenantID, p.MailboxID, book.Slug, book.DisplayName, book.Description))
		return err
	})
	return book, err
}

func (r *Repository) DeleteAddressbook(ctx context.Context, p domain.Principal, slug string) error {
	return r.scoped(ctx, p, func(ctx context.Context) error {
		tag, err := r.pool.Exec(ctx, `DELETE FROM mail_dav.addressbooks WHERE tenant_id = $1 AND mailbox_id = $2 AND slug = $3`, p.TenantID, p.MailboxID, slug)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return domain.ErrNotFound
		}
		return nil
	})
}

func (r *Repository) ListContacts(ctx context.Context, p domain.Principal, slug string) (domain.Addressbook, []domain.Contact, error) {
	var (
		book domain.Addressbook
		out  []domain.Contact
	)
	err := r.scoped(ctx, p, func(ctx context.Context) (err error) {
		if book, err = r.book(ctx, p, slug, false); err != nil {
			return err
		}
		out, err = r.contacts(ctx, p, book, nil)
		return err
	})
	return book, out, err
}

func (r *Repository) GetContact(ctx context.Context, p domain.Principal, slug, resource string) (domain.Contact, error) {
	var out domain.Contact
	err := r.scoped(ctx, p, func(ctx context.Context) error {
		book, err := r.book(ctx, p, slug, false)
		if err != nil {
			return err
		}
		out, err = scanContact(r.pool.QueryRow(ctx,
			`SELECT `+contactColumns+` FROM mail_dav.contacts
			  WHERE tenant_id = $1 AND mailbox_id = $2 AND addressbook_id = $3 AND resource_name = $4`,
			p.TenantID, p.MailboxID, book.ID, resource))
		return err
	})
	return out, err
}

func (r *Repository) GetContacts(ctx context.Context, p domain.Principal, slug string, resources []string) ([]domain.Contact, error) {
	var out []domain.Contact
	err := r.scoped(ctx, p, func(ctx context.Context) error {
		book, err := r.book(ctx, p, slug, false)
		if err != nil {
			return err
		}
		out, err = r.contacts(ctx, p, book, resources)
		return err
	})
	return out, err
}

func (r *Repository) existingETag(ctx context.Context, book domain.Addressbook, resource string) (*string, error) {
	var etag string
	err := r.pool.QueryRow(ctx, `SELECT etag FROM mail_dav.contacts WHERE addressbook_id = $1 AND resource_name = $2`, book.ID, resource).Scan(&etag)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &etag, nil
}

// recordChange avanza la secuencia de la libreta (su ctag), anota el cambio y poda lo que ya no se
// conserva. Corre con la libreta bloqueada, asi que las secuencias no se cruzan.
func (r *Repository) recordChange(ctx context.Context, p domain.Principal, book domain.Addressbook, resource string, deleted bool, maxChanges int) error {
	var seq, floor int64
	if err := r.pool.QueryRow(ctx,
		`UPDATE mail_dav.addressbooks SET sync_seq = sync_seq + 1, changes_floor = GREATEST(changes_floor, sync_seq + 1 - $2)
		  WHERE id = $1 RETURNING sync_seq, changes_floor`, book.ID, maxChanges).Scan(&seq, &floor); err != nil {
		return err
	}
	if _, err := r.pool.Exec(ctx,
		`INSERT INTO mail_dav.collection_changes (addressbook_id, seq, tenant_id, mailbox_id, resource_name, deleted)
		 VALUES ($1, $2, $3, $4, $5, $6)`, book.ID, seq, p.TenantID, p.MailboxID, resource, deleted); err != nil {
		return err
	}
	_, err := r.pool.Exec(ctx, `DELETE FROM mail_dav.collection_changes WHERE addressbook_id = $1 AND seq <= $2`, book.ID, floor)
	return err
}

func (r *Repository) PutContact(ctx context.Context, p domain.Principal, slug string, c domain.Contact, cond domain.Precondition, maxContacts, maxChanges int) (bool, error) {
	var created bool
	err := r.scoped(ctx, p, func(ctx context.Context) error {
		book, err := r.book(ctx, p, slug, true)
		if err != nil {
			return err
		}
		current, err := r.existingETag(ctx, book, c.ResourceName)
		if err != nil {
			return err
		}
		if err := cond.Check(current); err != nil {
			return err
		}
		if current != nil && *current == c.ETag {
			return nil
		}
		var conflict string
		err = r.pool.QueryRow(ctx,
			`SELECT resource_name FROM mail_dav.contacts WHERE addressbook_id = $1 AND uid = $2 AND resource_name <> $3`,
			book.ID, c.UID, c.ResourceName).Scan(&conflict)
		if err == nil {
			return &domain.UIDConflictError{Resource: conflict}
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		emails := c.Emails
		if emails == nil {
			emails = []string{}
		}
		if current == nil {
			if err := r.lockMailbox(ctx, p); err != nil {
				return err
			}
			var count int
			if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM mail_dav.contacts WHERE tenant_id = $1 AND mailbox_id = $2`, p.TenantID, p.MailboxID).Scan(&count); err != nil {
				return err
			}
			if count >= maxContacts {
				return domain.ErrContactLimit
			}
			_, err = r.pool.Exec(ctx,
				`INSERT INTO mail_dav.contacts (id, tenant_id, mailbox_id, addressbook_id, resource_name, uid, vcard, etag, display_name, emails)
				 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
				c.ID, p.TenantID, p.MailboxID, book.ID, c.ResourceName, c.UID, c.VCard, c.ETag, c.DisplayName, emails)
			created = true
		} else {
			_, err = r.pool.Exec(ctx,
				`UPDATE mail_dav.contacts SET uid = $4, vcard = $5, etag = $6, display_name = $7, emails = $8
				  WHERE addressbook_id = $1 AND tenant_id = $2 AND mailbox_id = $3 AND resource_name = $9`,
				book.ID, p.TenantID, p.MailboxID, c.UID, c.VCard, c.ETag, c.DisplayName, emails, c.ResourceName)
		}
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
				return &domain.UIDConflictError{}
			}
			return err
		}
		return r.recordChange(ctx, p, book, c.ResourceName, false, maxChanges)
	})
	return created && err == nil, err
}

func (r *Repository) DeleteContact(ctx context.Context, p domain.Principal, slug, resource string, cond domain.Precondition, maxChanges int) error {
	return r.scoped(ctx, p, func(ctx context.Context) error {
		book, err := r.book(ctx, p, slug, true)
		if err != nil {
			return err
		}
		current, err := r.existingETag(ctx, book, resource)
		if err != nil {
			return err
		}
		if current == nil {
			return domain.ErrNotFound
		}
		if err := cond.Check(current); err != nil {
			return err
		}
		if _, err := r.pool.Exec(ctx, `DELETE FROM mail_dav.contacts WHERE addressbook_id = $1 AND tenant_id = $2 AND mailbox_id = $3 AND resource_name = $4`,
			book.ID, p.TenantID, p.MailboxID, resource); err != nil {
			return err
		}
		return r.recordChange(ctx, p, book, resource, true, maxChanges)
	})
}

func (r *Repository) ChangesSince(ctx context.Context, p domain.Principal, slug string, seq int64) (domain.Addressbook, []domain.Contact, []string, error) {
	var (
		book    domain.Addressbook
		changed []domain.Contact
		removed []string
	)
	err := r.scoped(ctx, p, func(ctx context.Context) (err error) {
		if book, err = r.book(ctx, p, slug, false); err != nil {
			return err
		}
		if seq < book.ChangesFloor || seq > book.SyncSeq {
			return domain.ErrInvalidSyncToken
		}
		rows, err := r.pool.Query(ctx,
			`SELECT DISTINCT ON (resource_name) resource_name, deleted FROM mail_dav.collection_changes
			  WHERE addressbook_id = $1 AND tenant_id = $2 AND mailbox_id = $3 AND seq > $4 AND seq <= $5
			  ORDER BY resource_name, seq DESC`, book.ID, p.TenantID, p.MailboxID, seq, book.SyncSeq)
		if err != nil {
			return err
		}
		defer rows.Close()
		var live []string
		for rows.Next() {
			var name string
			var deleted bool
			if err := rows.Scan(&name, &deleted); err != nil {
				return err
			}
			if deleted {
				removed = append(removed, name)
			} else {
				live = append(live, name)
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()
		if len(live) > 0 {
			changed, err = r.contacts(ctx, p, book, live)
		}
		return err
	})
	sort.Strings(removed)
	return book, changed, removed, err
}
