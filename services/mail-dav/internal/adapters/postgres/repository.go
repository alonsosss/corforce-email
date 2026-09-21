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

// collectionKind nombra las tablas de un tipo de coleccion: las libretas y los calendarios comparten el
// codigo de coleccion (alta, baja, cambios, escritura condicional) y solo difieren en sus tablas. Son
// constantes de este paquete, nunca texto del cliente.
type collectionKind struct {
	table   string
	items   string
	changes string
	// fk es la columna de items y changes que apunta a la coleccion.
	fk string
	// data es la columna con el objeto (vCard o iCalendar), cuyo tamano cuenta para el espacio del buzon.
	data string
	// limit y itemLimit son los errores al pasar el maximo de colecciones y de objetos del buzon.
	limit     error
	itemLimit error
}

var (
	addressbooksKind = collectionKind{table: "mail_dav.addressbooks", items: "mail_dav.contacts", changes: "mail_dav.collection_changes", fk: "addressbook_id", data: "vcard", limit: domain.ErrAddressbookLimit, itemLimit: domain.ErrContactLimit}
	calendarsKind    = collectionKind{table: "mail_dav.calendars", items: "mail_dav.events", changes: "mail_dav.calendar_changes", fk: "calendar_id", data: "ical", limit: domain.ErrCalendarLimit, itemLimit: domain.ErrEventLimit}
)

// mailboxLockClass es el espacio de los cerrojos consultivos por buzon: serializa el conteo de los
// limites (colecciones y objetos) y el alta que lo sigue.
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

const collectionColumns = `id, tenant_id, mailbox_id, slug, display_name, description, sync_seq, changes_floor, created_at, updated_at`

func scanCollection(row pgx.Row) (domain.Addressbook, error) {
	var b domain.Addressbook
	err := row.Scan(&b.ID, &b.TenantID, &b.MailboxID, &b.Slug, &b.DisplayName, &b.Description, &b.SyncSeq, &b.ChangesFloor, &b.CreatedAt, &b.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return b, domain.ErrNotFound
	}
	return b, err
}

// contactColumns son las columnas de un contacto; sin datos el vCard se lee vacio y solo se trae su
// tamano (octet_length de un text no descomprime el valor).
func contactColumns(withData bool) string {
	data := `''`
	if withData {
		data = `vcard`
	}
	return `id, tenant_id, mailbox_id, addressbook_id, resource_name, uid, ` + data + `, etag, display_name, emails, created_at, updated_at, octet_length(vcard)`
}

func scanContact(row pgx.Row) (domain.Contact, error) {
	var c domain.Contact
	err := row.Scan(&c.ID, &c.TenantID, &c.MailboxID, &c.AddressbookID, &c.ResourceName, &c.UID, &c.VCard, &c.ETag, &c.DisplayName, &c.Emails, &c.CreatedAt, &c.UpdatedAt, &c.Size)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, domain.ErrNotFound
	}
	return c, err
}

func (r *Repository) collection(ctx context.Context, p domain.Principal, k collectionKind, slug string, forUpdate bool) (domain.Addressbook, error) {
	q := `SELECT ` + collectionColumns + ` FROM ` + k.table + ` WHERE tenant_id = $1 AND mailbox_id = $2 AND slug = $3`
	if forUpdate {
		q += ` FOR UPDATE`
	}
	return scanCollection(r.pool.QueryRow(ctx, q, p.TenantID, p.MailboxID, slug))
}

// readBudget corta un listado que lleva objetos al pasar el tope de bytes: la memoria de una lectura no
// puede depender de lo que el buzon tenga guardado.
type readBudget struct {
	max, used int
}

func (b *readBudget) add(opt domain.ReadOptions, size int) error {
	if !opt.WithData || b.max <= 0 {
		return nil
	}
	if b.used += size; b.used > b.max {
		return domain.ErrResultTooLarge
	}
	return nil
}

func (r *Repository) contactsQuery(book domain.Addressbook, p domain.Principal, names []string, withData bool) (string, []any) {
	q := `SELECT ` + contactColumns(withData) + ` FROM mail_dav.contacts WHERE tenant_id = $1 AND mailbox_id = $2 AND addressbook_id = $3`
	args := []any{p.TenantID, p.MailboxID, book.ID}
	if names != nil {
		q += ` AND resource_name = ANY($4)`
		args = append(args, names)
	}
	return q + ` ORDER BY resource_name`, args
}

func (r *Repository) contacts(ctx context.Context, p domain.Principal, book domain.Addressbook, names []string, opt domain.ReadOptions) ([]domain.Contact, error) {
	q, args := r.contactsQuery(book, p, names, opt.WithData)
	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	budget := readBudget{max: opt.MaxBytes}
	out := []domain.Contact{}
	for rows.Next() {
		c, err := scanContact(rows)
		if err != nil {
			return nil, err
		}
		if err := budget.add(opt, c.Size); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *Repository) listCollections(ctx context.Context, p domain.Principal, k collectionKind) ([]domain.Addressbook, error) {
	out := []domain.Addressbook{}
	err := r.scoped(ctx, p, func(ctx context.Context) error {
		rows, err := r.pool.Query(ctx, `SELECT `+collectionColumns+` FROM `+k.table+` WHERE tenant_id = $1 AND mailbox_id = $2 ORDER BY slug`, p.TenantID, p.MailboxID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			b, err := scanCollection(rows)
			if err != nil {
				return err
			}
			out = append(out, b)
		}
		return rows.Err()
	})
	return out, err
}

func (r *Repository) getCollection(ctx context.Context, p domain.Principal, k collectionKind, slug string) (domain.Addressbook, error) {
	var out domain.Addressbook
	err := r.scoped(ctx, p, func(ctx context.Context) (err error) {
		out, err = r.collection(ctx, p, k, slug, false)
		return err
	})
	return out, err
}

func (r *Repository) createCollection(ctx context.Context, p domain.Principal, k collectionKind, in domain.Addressbook, maxCollections int) (domain.Addressbook, error) {
	err := r.scoped(ctx, p, func(ctx context.Context) error {
		if err := r.lockMailbox(ctx, p); err != nil {
			return err
		}
		if _, err := r.collection(ctx, p, k, in.Slug, false); err == nil {
			return domain.ErrAlreadyExists
		} else if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		var count int
		if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM `+k.table+` WHERE tenant_id = $1 AND mailbox_id = $2`, p.TenantID, p.MailboxID).Scan(&count); err != nil {
			return err
		}
		if count >= maxCollections {
			return k.limit
		}
		var err error
		in, err = scanCollection(r.pool.QueryRow(ctx,
			`INSERT INTO `+k.table+` (id, tenant_id, mailbox_id, slug, display_name, description)
			 VALUES ($1, $2, $3, $4, $5, $6) RETURNING `+collectionColumns,
			in.ID, p.TenantID, p.MailboxID, in.Slug, in.DisplayName, in.Description))
		return err
	})
	return in, err
}

func (r *Repository) deleteCollection(ctx context.Context, p domain.Principal, k collectionKind, slug string) error {
	return r.scoped(ctx, p, func(ctx context.Context) error {
		tag, err := r.pool.Exec(ctx, `DELETE FROM `+k.table+` WHERE tenant_id = $1 AND mailbox_id = $2 AND slug = $3`, p.TenantID, p.MailboxID, slug)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return domain.ErrNotFound
		}
		return nil
	})
}

// deleteMailboxCollections borra las colecciones del buzon; sus objetos y su registro de cambios caen por
// la clave foranea compuesta (ON DELETE CASCADE).
func (r *Repository) deleteMailboxCollections(ctx context.Context, p domain.Principal, k collectionKind) (int, error) {
	var removed int
	err := r.scoped(ctx, p, func(ctx context.Context) error {
		if err := r.lockMailbox(ctx, p); err != nil {
			return err
		}
		tag, err := r.pool.Exec(ctx, `DELETE FROM `+k.table+` WHERE tenant_id = $1 AND mailbox_id = $2`, p.TenantID, p.MailboxID)
		if err != nil {
			return err
		}
		removed = int(tag.RowsAffected())
		return nil
	})
	return removed, err
}

func (r *Repository) ListAddressbooks(ctx context.Context, p domain.Principal) ([]domain.Addressbook, error) {
	return r.listCollections(ctx, p, addressbooksKind)
}

func (r *Repository) GetAddressbook(ctx context.Context, p domain.Principal, slug string) (domain.Addressbook, error) {
	return r.getCollection(ctx, p, addressbooksKind, slug)
}

func (r *Repository) CreateAddressbook(ctx context.Context, p domain.Principal, book domain.Addressbook, maxBooks int) (domain.Addressbook, error) {
	return r.createCollection(ctx, p, addressbooksKind, book, maxBooks)
}

func (r *Repository) DeleteAddressbook(ctx context.Context, p domain.Principal, slug string) error {
	return r.deleteCollection(ctx, p, addressbooksKind, slug)
}

// DeleteMailboxData borra las libretas del buzon.
func (r *Repository) DeleteMailboxData(ctx context.Context, p domain.Principal) (int, error) {
	return r.deleteMailboxCollections(ctx, p, addressbooksKind)
}

func (r *Repository) ListContacts(ctx context.Context, p domain.Principal, slug string, opt domain.ReadOptions) (domain.Addressbook, []domain.Contact, error) {
	var (
		book domain.Addressbook
		out  []domain.Contact
	)
	err := r.scoped(ctx, p, func(ctx context.Context) (err error) {
		if book, err = r.collection(ctx, p, addressbooksKind, slug, false); err != nil {
			return err
		}
		out, err = r.contacts(ctx, p, book, nil, opt)
		return err
	})
	return book, out, err
}

func (r *Repository) EachContact(ctx context.Context, p domain.Principal, slug string, fn func(domain.Contact) (bool, error)) (domain.Addressbook, error) {
	var book domain.Addressbook
	err := r.scoped(ctx, p, func(ctx context.Context) (err error) {
		if book, err = r.collection(ctx, p, addressbooksKind, slug, false); err != nil {
			return err
		}
		q, args := r.contactsQuery(book, p, nil, true)
		rows, err := r.pool.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanContact(rows)
			if err != nil {
				return err
			}
			if more, err := fn(c); err != nil || !more {
				return err
			}
		}
		return rows.Err()
	})
	return book, err
}

func (r *Repository) GetContact(ctx context.Context, p domain.Principal, slug, resource string) (domain.Contact, error) {
	var out domain.Contact
	err := r.scoped(ctx, p, func(ctx context.Context) error {
		book, err := r.collection(ctx, p, addressbooksKind, slug, false)
		if err != nil {
			return err
		}
		out, err = scanContact(r.pool.QueryRow(ctx,
			`SELECT `+contactColumns(true)+` FROM mail_dav.contacts
			  WHERE tenant_id = $1 AND mailbox_id = $2 AND addressbook_id = $3 AND resource_name = $4`,
			p.TenantID, p.MailboxID, book.ID, resource))
		return err
	})
	return out, err
}

func (r *Repository) GetContacts(ctx context.Context, p domain.Principal, slug string, resources []string, opt domain.ReadOptions) ([]domain.Contact, error) {
	var out []domain.Contact
	err := r.scoped(ctx, p, func(ctx context.Context) error {
		book, err := r.collection(ctx, p, addressbooksKind, slug, false)
		if err != nil {
			return err
		}
		out, err = r.contacts(ctx, p, book, resources, opt)
		return err
	})
	return out, err
}

func (r *Repository) existingETag(ctx context.Context, k collectionKind, coll domain.Addressbook, resource string) (*string, error) {
	var etag string
	err := r.pool.QueryRow(ctx, `SELECT etag FROM `+k.items+` WHERE `+k.fk+` = $1 AND resource_name = $2`, coll.ID, resource).Scan(&etag)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &etag, nil
}

// recordChange avanza la secuencia de la coleccion (su ctag), anota el cambio y poda lo que ya no se
// conserva. Corre con la coleccion bloqueada, asi que las secuencias no se cruzan.
func (r *Repository) recordChange(ctx context.Context, p domain.Principal, k collectionKind, coll domain.Addressbook, resource string, deleted bool, maxChanges int) error {
	var seq, floor int64
	if err := r.pool.QueryRow(ctx,
		`UPDATE `+k.table+` SET sync_seq = sync_seq + 1, changes_floor = GREATEST(changes_floor, sync_seq + 1 - $2)
		  WHERE id = $1 RETURNING sync_seq, changes_floor`, coll.ID, maxChanges).Scan(&seq, &floor); err != nil {
		return err
	}
	if _, err := r.pool.Exec(ctx,
		`INSERT INTO `+k.changes+` (`+k.fk+`, seq, tenant_id, mailbox_id, resource_name, deleted)
		 VALUES ($1, $2, $3, $4, $5, $6)`, coll.ID, seq, p.TenantID, p.MailboxID, resource, deleted); err != nil {
		return err
	}
	_, err := r.pool.Exec(ctx, `DELETE FROM `+k.changes+` WHERE `+k.fk+` = $1 AND seq <= $2`, coll.ID, floor)
	return err
}

// itemWrite es lo que cambia de un objeto a otro en una escritura: la identidad del recurso y las dos
// sentencias, que solo conocen sus columnas. El resto del flujo (bloqueo, precondiciones, UID unico,
// limite, registro de cambios) es comun.
type itemWrite struct {
	resource string
	uid      string
	etag     string
	size     int64
	insert   func(ctx context.Context, coll domain.Addressbook) error
	update   func(ctx context.Context, coll domain.Addressbook) error
}

// storageExceeded dice si guardar w dejaria al buzon por encima de su espacio. Reemplazar un objeto por
// otro que no es mayor nunca se rechaza: un buzon que ya estaba por encima puede reducir lo suyo.
func (r *Repository) storageExceeded(ctx context.Context, p domain.Principal, k collectionKind, coll domain.Addressbook, w itemWrite, maxBytes int64) (bool, error) {
	var total, own int64
	err := r.pool.QueryRow(ctx,
		`SELECT COALESCE(sum(octet_length(`+k.data+`)), 0),
		        COALESCE(sum(octet_length(`+k.data+`)) FILTER (WHERE `+k.fk+` = $3 AND resource_name = $4), 0)
		   FROM `+k.items+` WHERE tenant_id = $1 AND mailbox_id = $2`,
		p.TenantID, p.MailboxID, coll.ID, w.resource).Scan(&total, &own)
	if err != nil {
		return false, err
	}
	return total-own+w.size > maxBytes && w.size > own, nil
}

// putItem escribe con el cerrojo del buzon tomado ANTES que el de la coleccion: es el mismo orden que sigue
// la baja de los datos de un buzon, de modo que ninguna escritura y ninguna baja se esperan la una a la otra.
func (r *Repository) putItem(ctx context.Context, p domain.Principal, k collectionKind, slug string, w itemWrite, cond domain.Precondition, lim domain.WriteLimits) (bool, error) {
	var created bool
	err := r.scoped(ctx, p, func(ctx context.Context) error {
		if err := r.lockMailbox(ctx, p); err != nil {
			return err
		}
		coll, err := r.collection(ctx, p, k, slug, true)
		if err != nil {
			return err
		}
		current, err := r.existingETag(ctx, k, coll, w.resource)
		if err != nil {
			return err
		}
		if err := cond.Check(current); err != nil {
			return err
		}
		if current != nil && *current == w.etag {
			return nil
		}
		var conflict string
		err = r.pool.QueryRow(ctx,
			`SELECT resource_name FROM `+k.items+` WHERE `+k.fk+` = $1 AND uid = $2 AND resource_name <> $3`,
			coll.ID, w.uid, w.resource).Scan(&conflict)
		if err == nil {
			return &domain.UIDConflictError{Resource: conflict}
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if exceeded, err := r.storageExceeded(ctx, p, k, coll, w, lim.MaxBytes); err != nil {
			return err
		} else if exceeded {
			return domain.ErrStorageLimit
		}
		if current == nil {
			var count int
			if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM `+k.items+` WHERE tenant_id = $1 AND mailbox_id = $2`, p.TenantID, p.MailboxID).Scan(&count); err != nil {
				return err
			}
			if count >= lim.MaxItems {
				return k.itemLimit
			}
			err = w.insert(ctx, coll)
			created = true
		} else {
			err = w.update(ctx, coll)
		}
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
				return &domain.UIDConflictError{}
			}
			return err
		}
		return r.recordChange(ctx, p, k, coll, w.resource, false, lim.MaxChanges)
	})
	return created && err == nil, err
}

func (r *Repository) deleteItem(ctx context.Context, p domain.Principal, k collectionKind, slug, resource string, cond domain.Precondition, maxChanges int) error {
	return r.scoped(ctx, p, func(ctx context.Context) error {
		coll, err := r.collection(ctx, p, k, slug, true)
		if err != nil {
			return err
		}
		current, err := r.existingETag(ctx, k, coll, resource)
		if err != nil {
			return err
		}
		if current == nil {
			return domain.ErrNotFound
		}
		if err := cond.Check(current); err != nil {
			return err
		}
		if _, err := r.pool.Exec(ctx, `DELETE FROM `+k.items+` WHERE `+k.fk+` = $1 AND tenant_id = $2 AND mailbox_id = $3 AND resource_name = $4`,
			coll.ID, p.TenantID, p.MailboxID, resource); err != nil {
			return err
		}
		return r.recordChange(ctx, p, k, coll, resource, true, maxChanges)
	})
}

// changedNames devuelve los nombres de los objetos que existen y cambiaron despues de seq (live) y los de
// los borrados (removed). Con la coleccion ya leida: domain.ErrInvalidSyncToken si seq ya no se resuelve.
func (r *Repository) changedNames(ctx context.Context, p domain.Principal, k collectionKind, coll domain.Addressbook, seq int64) (live, removed []string, err error) {
	if seq < coll.ChangesFloor || seq > coll.SyncSeq {
		return nil, nil, domain.ErrInvalidSyncToken
	}
	rows, err := r.pool.Query(ctx,
		`SELECT DISTINCT ON (resource_name) resource_name, deleted FROM `+k.changes+`
		  WHERE `+k.fk+` = $1 AND tenant_id = $2 AND mailbox_id = $3 AND seq > $4 AND seq <= $5
		  ORDER BY resource_name, seq DESC`, coll.ID, p.TenantID, p.MailboxID, seq, coll.SyncSeq)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var deleted bool
		if err := rows.Scan(&name, &deleted); err != nil {
			return nil, nil, err
		}
		if deleted {
			removed = append(removed, name)
		} else {
			live = append(live, name)
		}
	}
	sort.Strings(removed)
	return live, removed, rows.Err()
}

func (r *Repository) PutContact(ctx context.Context, p domain.Principal, slug string, c domain.Contact, cond domain.Precondition, lim domain.WriteLimits) (bool, error) {
	emails := c.Emails
	if emails == nil {
		emails = []string{}
	}
	return r.putItem(ctx, p, addressbooksKind, slug, itemWrite{
		resource: c.ResourceName, uid: c.UID, etag: c.ETag, size: int64(len(c.VCard)),
		insert: func(ctx context.Context, book domain.Addressbook) error {
			_, err := r.pool.Exec(ctx,
				`INSERT INTO mail_dav.contacts (id, tenant_id, mailbox_id, addressbook_id, resource_name, uid, vcard, etag, display_name, emails)
				 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
				c.ID, p.TenantID, p.MailboxID, book.ID, c.ResourceName, c.UID, c.VCard, c.ETag, c.DisplayName, emails)
			return err
		},
		update: func(ctx context.Context, book domain.Addressbook) error {
			_, err := r.pool.Exec(ctx,
				`UPDATE mail_dav.contacts SET uid = $4, vcard = $5, etag = $6, display_name = $7, emails = $8
				  WHERE addressbook_id = $1 AND tenant_id = $2 AND mailbox_id = $3 AND resource_name = $9`,
				book.ID, p.TenantID, p.MailboxID, c.UID, c.VCard, c.ETag, c.DisplayName, emails, c.ResourceName)
			return err
		},
	}, cond, lim)
}

func (r *Repository) DeleteContact(ctx context.Context, p domain.Principal, slug, resource string, cond domain.Precondition, maxChanges int) error {
	return r.deleteItem(ctx, p, addressbooksKind, slug, resource, cond, maxChanges)
}

func (r *Repository) ChangesSince(ctx context.Context, p domain.Principal, slug string, seq int64, opt domain.ReadOptions) (domain.Addressbook, []domain.Contact, []string, error) {
	var (
		book    domain.Addressbook
		changed []domain.Contact
		removed []string
	)
	err := r.scoped(ctx, p, func(ctx context.Context) error {
		var err error
		if book, err = r.collection(ctx, p, addressbooksKind, slug, false); err != nil {
			return err
		}
		var live []string
		if live, removed, err = r.changedNames(ctx, p, addressbooksKind, book, seq); err != nil {
			return err
		}
		if len(live) > 0 {
			changed, err = r.contacts(ctx, p, book, live, opt)
		}
		return err
	})
	return book, changed, removed, err
}
