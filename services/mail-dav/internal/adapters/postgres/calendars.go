package postgres

import (
	"context"
	"errors"
	"strconv"

	"github.com/alonsosss/corforce-email/services/mail-dav/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// eventColumns son las columnas de un evento; sin datos el iCalendar se lee vacio y solo se trae su tamano.
func eventColumns(withData bool) string {
	data := `''`
	if withData {
		data = `ical`
	}
	return `id, tenant_id, mailbox_id, calendar_id, resource_name, uid, ` + data + `, etag, summary, first_start, last_end, created_at, updated_at, octet_length(ical)`
}

func scanEvent(row pgx.Row) (domain.Event, error) {
	var e domain.Event
	err := row.Scan(&e.ID, &e.TenantID, &e.MailboxID, &e.CalendarID, &e.ResourceName, &e.UID, &e.ICal, &e.ETag, &e.Summary, &e.FirstStart, &e.LastEnd, &e.CreatedAt, &e.UpdatedAt, &e.Size)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, domain.ErrNotFound
	}
	return e, err
}

// eventsQuery arma la lectura de los eventos del calendario. names acota por nombre de recurso (nil: todos)
// y window descarta por tiempo con los indices de inicio y fin: un evento sin fin conocido (last_end nulo)
// nunca se descarta.
func (r *Repository) eventsQuery(p domain.Principal, cal domain.Calendar, names []string, window domain.EventWindow, withData bool) (string, []any) {
	q := `SELECT ` + eventColumns(withData) + ` FROM mail_dav.events WHERE tenant_id = $1 AND mailbox_id = $2 AND calendar_id = $3`
	args := []any{p.TenantID, p.MailboxID, cal.ID}
	if names != nil {
		args = append(args, names)
		q += ` AND resource_name = ANY($` + strconv.Itoa(len(args)) + `)`
	}
	if window.End != nil {
		args = append(args, *window.End)
		q += ` AND first_start < $` + strconv.Itoa(len(args))
	}
	if window.Start != nil {
		args = append(args, *window.Start)
		q += ` AND (last_end IS NULL OR last_end >= $` + strconv.Itoa(len(args)) + `)`
	}
	return q + ` ORDER BY resource_name`, args
}

func (r *Repository) events(ctx context.Context, p domain.Principal, cal domain.Calendar, names []string, opt domain.ReadOptions) ([]domain.Event, error) {
	q, args := r.eventsQuery(p, cal, names, domain.EventWindow{}, opt.WithData)
	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	budget := readBudget{max: opt.MaxBytes}
	out := []domain.Event{}
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		if err := budget.add(opt, e.Size); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r *Repository) ListCalendars(ctx context.Context, p domain.Principal) ([]domain.Calendar, error) {
	return r.listCollections(ctx, p, calendarsKind)
}

func (r *Repository) GetCalendar(ctx context.Context, p domain.Principal, slug string) (domain.Calendar, error) {
	return r.getCollection(ctx, p, calendarsKind, slug)
}

func (r *Repository) CreateCalendar(ctx context.Context, p domain.Principal, cal domain.Calendar, maxCalendars int) (domain.Calendar, error) {
	return r.createCollection(ctx, p, calendarsKind, cal, maxCalendars)
}

func (r *Repository) DeleteCalendar(ctx context.Context, p domain.Principal, slug string) error {
	return r.deleteCollection(ctx, p, calendarsKind, slug)
}

// DeleteMailboxCalendars borra los calendarios del buzon.
func (r *Repository) DeleteMailboxCalendars(ctx context.Context, p domain.Principal) (int, error) {
	return r.deleteMailboxCollections(ctx, p, calendarsKind)
}

func (r *Repository) ListEvents(ctx context.Context, p domain.Principal, slug string, opt domain.ReadOptions) (domain.Calendar, []domain.Event, error) {
	var (
		cal domain.Calendar
		out []domain.Event
	)
	err := r.scoped(ctx, p, func(ctx context.Context) (err error) {
		if cal, err = r.collection(ctx, p, calendarsKind, slug, false); err != nil {
			return err
		}
		out, err = r.events(ctx, p, cal, nil, opt)
		return err
	})
	return cal, out, err
}

func (r *Repository) EachEvent(ctx context.Context, p domain.Principal, slug string, window domain.EventWindow, fn func(domain.Event) (bool, error)) (domain.Calendar, error) {
	var cal domain.Calendar
	err := r.scoped(ctx, p, func(ctx context.Context) (err error) {
		if cal, err = r.collection(ctx, p, calendarsKind, slug, false); err != nil {
			return err
		}
		q, args := r.eventsQuery(p, cal, nil, window, true)
		rows, err := r.pool.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			e, err := scanEvent(rows)
			if err != nil {
				return err
			}
			if more, err := fn(e); err != nil || !more {
				return err
			}
		}
		return rows.Err()
	})
	return cal, err
}

func (r *Repository) GetEvent(ctx context.Context, p domain.Principal, slug, resource string) (domain.Event, error) {
	var out domain.Event
	err := r.scoped(ctx, p, func(ctx context.Context) error {
		cal, err := r.collection(ctx, p, calendarsKind, slug, false)
		if err != nil {
			return err
		}
		out, err = scanEvent(r.pool.QueryRow(ctx,
			`SELECT `+eventColumns(true)+` FROM mail_dav.events
			  WHERE tenant_id = $1 AND mailbox_id = $2 AND calendar_id = $3 AND resource_name = $4`,
			p.TenantID, p.MailboxID, cal.ID, resource))
		return err
	})
	return out, err
}

func (r *Repository) GetEvents(ctx context.Context, p domain.Principal, slug string, resources []string, opt domain.ReadOptions) ([]domain.Event, error) {
	var out []domain.Event
	err := r.scoped(ctx, p, func(ctx context.Context) error {
		cal, err := r.collection(ctx, p, calendarsKind, slug, false)
		if err != nil {
			return err
		}
		out, err = r.events(ctx, p, cal, resources, opt)
		return err
	})
	return out, err
}

func (r *Repository) PutEvent(ctx context.Context, p domain.Principal, slug string, e domain.Event, cond domain.Precondition, lim domain.WriteLimits) (bool, error) {
	return r.putItem(ctx, p, calendarsKind, slug, r.eventWrite(p, e), cond, lim)
}

// eventWrite es la escritura de un evento con su ocupacion: la materializada (e.BusyPlanned) o, sin ella, la
// marca de pendiente (busy_until = -infinity) para que la complete la primera consulta de disponibilidad.
func (r *Repository) eventWrite(p domain.Principal, e domain.Event) itemWrite {
	return itemWrite{
		resource: e.ResourceName, uid: e.UID, etag: e.ETag, size: int64(len(e.ICal)),
		insert: func(ctx context.Context, cal domain.Calendar) error {
			_, err := r.pool.Exec(ctx,
				`INSERT INTO mail_dav.events (id, tenant_id, mailbox_id, calendar_id, resource_name, uid, ical, etag, summary, first_start, last_end)
				 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
				e.ID, p.TenantID, p.MailboxID, cal.ID, e.ResourceName, e.UID, e.ICal, e.ETag, e.Summary, e.FirstStart, e.LastEnd)
			return err
		},
		update: func(ctx context.Context, cal domain.Calendar) error {
			_, err := r.pool.Exec(ctx,
				`UPDATE mail_dav.events SET uid = $4, ical = $5, etag = $6, summary = $7, first_start = $8, last_end = $9
				  WHERE calendar_id = $1 AND tenant_id = $2 AND mailbox_id = $3 AND resource_name = $10`,
				cal.ID, p.TenantID, p.MailboxID, e.UID, e.ICal, e.ETag, e.Summary, e.FirstStart, e.LastEnd, e.ResourceName)
			return err
		},
		after: func(ctx context.Context, cal domain.Calendar) error {
			var id uuid.UUID
			if err := r.pool.QueryRow(ctx,
				`SELECT id FROM mail_dav.events WHERE calendar_id = $1 AND tenant_id = $2 AND mailbox_id = $3 AND resource_name = $4`,
				cal.ID, p.TenantID, p.MailboxID, e.ResourceName).Scan(&id); err != nil {
				return err
			}
			return r.writeBusy(ctx, p, id, e.BusyPlanned, e.Busy, e.BusyUntil)
		},
	}
}

func (r *Repository) DeleteEvent(ctx context.Context, p domain.Principal, slug, resource string, cond domain.Precondition, maxChanges int) error {
	return r.deleteItem(ctx, p, calendarsKind, slug, resource, cond, maxChanges)
}

func (r *Repository) EventChangesSince(ctx context.Context, p domain.Principal, slug string, seq int64, opt domain.ReadOptions) (domain.Calendar, []domain.Event, []string, error) {
	var (
		cal     domain.Calendar
		changed []domain.Event
		removed []string
	)
	err := r.scoped(ctx, p, func(ctx context.Context) error {
		var err error
		if cal, err = r.collection(ctx, p, calendarsKind, slug, false); err != nil {
			return err
		}
		var live []string
		if live, removed, err = r.changedNames(ctx, p, calendarsKind, cal, seq); err != nil {
			return err
		}
		if len(live) > 0 {
			changed, err = r.events(ctx, p, cal, live, opt)
		}
		return err
	})
	return cal, changed, removed, err
}
