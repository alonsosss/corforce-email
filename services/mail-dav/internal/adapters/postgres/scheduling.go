package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-dav/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Planificacion (migracion 05_scheduling.sql): la ocupacion materializada de los eventos, la direccion de cada
// buzon, las paginas de citas y sus reservas. Lo que cruza buzones pasa por las funciones SECURITY DEFINER de la
// migracion; lo demas, como el resto del repositorio, con la identidad del buzon y sus politicas de fila.

// writeBusy reemplaza la ocupacion materializada de un evento dentro de la transaccion de su escritura.
func (r *Repository) writeBusy(ctx context.Context, p domain.Principal, eventID uuid.UUID, planned bool, busy []domain.Interval, until *time.Time) error {
	if _, err := r.pool.Exec(ctx, `DELETE FROM mail_dav.event_busy WHERE event_id = $1 AND tenant_id = $2 AND mailbox_id = $3`,
		eventID, p.TenantID, p.MailboxID); err != nil {
		return err
	}
	if !planned {
		_, err := r.pool.Exec(ctx, `UPDATE mail_dav.events SET busy_until = '-infinity' WHERE id = $1 AND tenant_id = $2 AND mailbox_id = $3`,
			eventID, p.TenantID, p.MailboxID)
		return err
	}
	if _, err := r.pool.Exec(ctx, `UPDATE mail_dav.events SET busy_until = $4 WHERE id = $1 AND tenant_id = $2 AND mailbox_id = $3`,
		eventID, p.TenantID, p.MailboxID, until); err != nil {
		return err
	}
	if len(busy) == 0 {
		return nil
	}
	starts := make([]time.Time, len(busy))
	ends := make([]time.Time, len(busy))
	for i, b := range busy {
		starts[i], ends[i] = b.Start, b.End
	}
	_, err := r.pool.Exec(ctx,
		`INSERT INTO mail_dav.event_busy (event_id, tenant_id, mailbox_id, starts_at, ends_at)
		 SELECT $1, $2, $3, s, e FROM unnest($4::timestamptz[], $5::timestamptz[]) AS t(s, e)`,
		eventID, p.TenantID, p.MailboxID, starts, ends)
	return err
}

// ReplaceBusy materializa de nuevo la ocupacion de un evento guardado si su etag sigue siendo etag: una escritura
// que se cruzo ya dejo la suya. Devuelve si la reemplazo.
func (r *Repository) ReplaceBusy(ctx context.Context, p domain.Principal, eventID uuid.UUID, etag string, busy []domain.Interval, until *time.Time) (bool, error) {
	replaced := false
	err := r.scoped(ctx, p, func(ctx context.Context) error {
		var id uuid.UUID
		err := r.pool.QueryRow(ctx,
			`SELECT id FROM mail_dav.events WHERE id = $1 AND tenant_id = $2 AND mailbox_id = $3 AND etag = $4 FOR UPDATE`,
			eventID, p.TenantID, p.MailboxID, etag).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		replaced = true
		return r.writeBusy(ctx, p, id, true, busy, until)
	})
	return replaced, err
}

// EventsByID lee con su iCalendar los eventos del buzon con esos ids, de cualquiera de sus calendarios.
func (r *Repository) EventsByID(ctx context.Context, p domain.Principal, ids []uuid.UUID) ([]domain.Event, error) {
	out := []domain.Event{}
	err := r.scoped(ctx, p, func(ctx context.Context) error {
		rows, err := r.pool.Query(ctx, `SELECT `+eventColumns(true)+` FROM mail_dav.events
			  WHERE tenant_id = $1 AND mailbox_id = $2 AND id = ANY($3) ORDER BY id`, p.TenantID, p.MailboxID, ids)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			e, err := scanEvent(rows)
			if err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	return out, err
}

// EventByUID lee el evento del calendario con ese UID.
func (r *Repository) EventByUID(ctx context.Context, p domain.Principal, slug, uid string) (domain.Event, error) {
	var out domain.Event
	err := r.scoped(ctx, p, func(ctx context.Context) error {
		cal, err := r.collection(ctx, p, calendarsKind, slug, false)
		if err != nil {
			return err
		}
		out, err = scanEvent(r.pool.QueryRow(ctx,
			`SELECT `+eventColumns(true)+` FROM mail_dav.events
			  WHERE tenant_id = $1 AND mailbox_id = $2 AND calendar_id = $3 AND uid = $4`,
			p.TenantID, p.MailboxID, cal.ID, uid))
		return err
	})
	return out, err
}

// RegisterAddress anota la direccion del buzon de la sesion (mail_dav.register_mailbox_address).
func (r *Repository) RegisterAddress(ctx context.Context, p domain.Principal, address string) error {
	return r.scoped(ctx, p, func(ctx context.Context) error {
		_, err := r.pool.Exec(ctx, `SELECT mail_dav.register_mailbox_address($1, $2, $3)`, p.TenantID, p.MailboxID, address)
		return err
	})
}

// ResolveMailboxes devuelve el buzon de la empresa de cada direccion que lo tiene registrado.
func (r *Repository) ResolveMailboxes(ctx context.Context, p domain.Principal, addresses []string) (map[string]uuid.UUID, error) {
	out := map[string]uuid.UUID{}
	err := r.scoped(ctx, p, func(ctx context.Context) error {
		rows, err := r.pool.Query(ctx, `SELECT address, mailbox_id FROM mail_dav.resolve_busy_mailboxes($1, $2)`, p.TenantID, addresses)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a string
			var id uuid.UUID
			if err := rows.Scan(&a, &id); err != nil {
				return err
			}
			out[a] = id
		}
		return rows.Err()
	})
	return out, err
}

// BusyIntervals devuelve la ocupacion materializada de esos buzones en [from, to): inicio y fin, nada mas.
func (r *Repository) BusyIntervals(ctx context.Context, p domain.Principal, mailboxes []uuid.UUID, from, to time.Time) (map[uuid.UUID][]domain.Interval, error) {
	out := map[uuid.UUID][]domain.Interval{}
	err := r.scoped(ctx, p, func(ctx context.Context) error {
		rows, err := r.pool.Query(ctx, `SELECT mailbox_id, starts_at, ends_at FROM mail_dav.busy_intervals($1, $2, $3, $4)`,
			p.TenantID, mailboxes, from, to)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id uuid.UUID
			var iv domain.Interval
			if err := rows.Scan(&id, &iv.Start, &iv.End); err != nil {
				return err
			}
			out[id] = append(out[id], iv)
		}
		return rows.Err()
	})
	return out, err
}

// PendingBusy devuelve, por buzon, los eventos cuya ocupacion no esta materializada hasta until.
func (r *Repository) PendingBusy(ctx context.Context, p domain.Principal, mailboxes []uuid.UUID, until time.Time, limit int) (map[uuid.UUID][]uuid.UUID, error) {
	out := map[uuid.UUID][]uuid.UUID{}
	err := r.scoped(ctx, p, func(ctx context.Context) error {
		rows, err := r.pool.Query(ctx, `SELECT mailbox_id, event_id FROM mail_dav.busy_pending_events($1, $2, $3, $4)`,
			p.TenantID, mailboxes, until, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var mb, ev uuid.UUID
			if err := rows.Scan(&mb, &ev); err != nil {
				return err
			}
			out[mb] = append(out[mb], ev)
		}
		return rows.Err()
	})
	return out, err
}

// weeklyJSON es la forma de las franjas en la base: {"MO": [["09:00", "13:00"]], ...}.
type weeklyJSON map[string][][2]string

func encodeWeekly(w [7][]domain.DayWindow) ([]byte, error) {
	out := weeklyJSON{}
	for d, day := range w {
		if len(day) == 0 {
			continue
		}
		code := domain.WeekdayCode(time.Weekday(d))
		for _, win := range day {
			out[code] = append(out[code], [2]string{domain.FormatClock(win.Start), domain.FormatClock(win.End)})
		}
	}
	return json.Marshal(out)
}

func decodeWeekly(raw []byte) ([7][]domain.DayWindow, error) {
	var out [7][]domain.DayWindow
	var in weeklyJSON
	if err := json.Unmarshal(raw, &in); err != nil {
		return out, err
	}
	for code, day := range in {
		d, ok := domain.WeekdayOf(code)
		if !ok {
			return out, fmt.Errorf("dia %q desconocido en las franjas", code)
		}
		for _, win := range day {
			s, err := domain.ParseClock("weekly", win[0])
			if err != nil {
				return out, err
			}
			e, err := domain.ParseClock("weekly", win[1])
			if err != nil {
				return out, err
			}
			out[d] = append(out[d], domain.DayWindow{Start: s, End: e})
		}
	}
	return out, nil
}

const bookingPageColumns = `id, tenant_id, mailbox_id, public_id, title, description, duration_minutes, buffer_minutes,
	min_notice_minutes, max_advance_days, daily_limit, timezone, weekly, active, owner_address, owner_name, created_at, updated_at`

func scanBookingPage(row pgx.Row) (domain.BookingPage, error) {
	var b domain.BookingPage
	var weekly []byte
	err := row.Scan(&b.ID, &b.TenantID, &b.MailboxID, &b.PublicID, &b.Title, &b.Description, &b.DurationMinutes, &b.BufferMinutes,
		&b.MinNoticeMinutes, &b.MaxAdvanceDays, &b.DailyLimit, &b.TimeZone, &weekly, &b.Active, &b.OwnerAddress, &b.OwnerName,
		&b.CreatedAt, &b.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return b, domain.ErrNotFound
	}
	if err != nil {
		return b, err
	}
	b.Weekly, err = decodeWeekly(weekly)
	return b, err
}

// BookingPage lee la pagina de citas del buzon.
func (r *Repository) BookingPage(ctx context.Context, p domain.Principal) (domain.BookingPage, error) {
	var out domain.BookingPage
	err := r.scoped(ctx, p, func(ctx context.Context) (err error) {
		out, err = scanBookingPage(r.pool.QueryRow(ctx, `SELECT `+bookingPageColumns+` FROM mail_dav.booking_pages
			  WHERE tenant_id = $1 AND mailbox_id = $2`, p.TenantID, p.MailboxID))
		return err
	})
	return out, err
}

// SaveBookingPage crea o reemplaza la pagina de citas del buzon (una por buzon).
func (r *Repository) SaveBookingPage(ctx context.Context, p domain.Principal, page domain.BookingPage) (domain.BookingPage, error) {
	weekly, err := encodeWeekly(page.Weekly)
	if err != nil {
		return domain.BookingPage{}, err
	}
	var out domain.BookingPage
	err = r.scoped(ctx, p, func(ctx context.Context) (err error) {
		out, err = scanBookingPage(r.pool.QueryRow(ctx,
			`INSERT INTO mail_dav.booking_pages (id, tenant_id, mailbox_id, public_id, title, description, duration_minutes, buffer_minutes,
			        min_notice_minutes, max_advance_days, daily_limit, timezone, weekly, active, owner_address, owner_name)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
			 ON CONFLICT (tenant_id, mailbox_id) DO UPDATE SET public_id = EXCLUDED.public_id, title = EXCLUDED.title,
			        description = EXCLUDED.description, duration_minutes = EXCLUDED.duration_minutes, buffer_minutes = EXCLUDED.buffer_minutes,
			        min_notice_minutes = EXCLUDED.min_notice_minutes, max_advance_days = EXCLUDED.max_advance_days,
			        daily_limit = EXCLUDED.daily_limit, timezone = EXCLUDED.timezone, weekly = EXCLUDED.weekly, active = EXCLUDED.active,
			        owner_address = EXCLUDED.owner_address, owner_name = EXCLUDED.owner_name
			 RETURNING `+bookingPageColumns,
			page.ID, p.TenantID, p.MailboxID, page.PublicID, page.Title, page.Description, page.DurationMinutes, page.BufferMinutes,
			page.MinNoticeMinutes, page.MaxAdvanceDays, page.DailyLimit, page.TimeZone, weekly, page.Active, page.OwnerAddress, page.OwnerName))
		return err
	})
	return out, err
}

// BookingPageOwner devuelve el buzon dueno de la pagina activa con ese enlace (uuid.Nil si no la hay). p lleva
// solo la empresa: la pagina publica no tiene buzon.
func (r *Repository) BookingPageOwner(ctx context.Context, p domain.Principal, publicID string) (uuid.UUID, error) {
	var owner *uuid.UUID
	err := r.scoped(ctx, p, func(ctx context.Context) error {
		return r.pool.QueryRow(ctx, `SELECT mail_dav.booking_page_owner($1, $2)`, p.TenantID, publicID).Scan(&owner)
	})
	if err != nil || owner == nil {
		return uuid.Nil, err
	}
	return *owner, nil
}

// ReserveBooking guarda la cita de una reserva en una sola transaccion, con el cerrojo del buzon dueno: comprueba
// los topes (reservas de la pagina en las ultimas 24 horas y del mismo visitante) y que el hueco, ensanchado por el
// margen, siga libre en la ocupacion materializada; despues guarda el evento con su ocupacion y anota la reserva.
// Las reservas de mas de dos dias se retiran: solo sirven para los topes.
func (r *Repository) ReserveBooking(ctx context.Context, p domain.Principal, slug string, e domain.Event, b domain.Booking, lim domain.BookingLimits) error {
	return r.scoped(ctx, p, func(ctx context.Context) error {
		if err := r.lockMailbox(ctx, p); err != nil {
			return err
		}
		if _, err := r.pool.Exec(ctx, `DELETE FROM mail_dav.bookings WHERE page_id = $1 AND tenant_id = $2 AND mailbox_id = $3 AND created_at < now() - interval '2 days'`,
			b.PageID, p.TenantID, p.MailboxID); err != nil {
			return err
		}
		var total, visitor int
		if err := r.pool.QueryRow(ctx,
			`SELECT count(*), count(*) FILTER (WHERE visitor_hash = $4) FROM mail_dav.bookings
			  WHERE page_id = $1 AND tenant_id = $2 AND mailbox_id = $3 AND created_at > now() - interval '24 hours'`,
			b.PageID, p.TenantID, p.MailboxID, b.VisitorHash).Scan(&total, &visitor); err != nil {
			return err
		}
		if total >= lim.Daily || visitor >= lim.PerVisitor {
			return domain.ErrBookingLimit
		}
		var taken bool
		if err := r.pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM mail_dav.event_busy WHERE tenant_id = $1 AND mailbox_id = $2 AND starts_at < $4 AND ends_at > $3)`,
			p.TenantID, p.MailboxID, b.Start.Add(-lim.Buffer), b.End.Add(lim.Buffer)).Scan(&taken); err != nil {
			return err
		}
		if taken {
			return domain.ErrSlotUnavailable
		}
		if _, err := r.putItemIn(ctx, p, calendarsKind, slug, r.eventWrite(p, e), domain.Precondition{IfNoneMatchAny: true}, lim.Write); err != nil {
			return err
		}
		_, err := r.pool.Exec(ctx,
			`INSERT INTO mail_dav.bookings (tenant_id, mailbox_id, page_id, event_resource, starts_at, ends_at, visitor_hash)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			p.TenantID, p.MailboxID, b.PageID, e.ResourceName, b.Start, b.End, b.VisitorHash)
		return err
	})
}

// DeleteMailboxScheduling retira la direccion y la pagina de citas (con sus reservas) de un buzon dado de baja.
// La ocupacion de sus eventos cae con ellos.
func (r *Repository) DeleteMailboxScheduling(ctx context.Context, p domain.Principal) error {
	return r.scoped(ctx, p, func(ctx context.Context) error {
		if _, err := r.pool.Exec(ctx, `DELETE FROM mail_dav.booking_pages WHERE tenant_id = $1 AND mailbox_id = $2`, p.TenantID, p.MailboxID); err != nil {
			return err
		}
		_, err := r.pool.Exec(ctx, `DELETE FROM mail_dav.mailbox_addresses WHERE tenant_id = $1 AND mailbox_id = $2`, p.TenantID, p.MailboxID)
		return err
	})
}
