package app

import (
	"context"
	"errors"

	"github.com/alonsosss/corforce-email/services/mail-dav/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// DefaultCalendarSlug es el calendario que se crea solo la primera vez que un buzon descubre sus calendarios.
const DefaultCalendarSlug = "calendar"

// Calendars lista los calendarios del buzon; si no tiene ninguno crea el de por defecto.
func (uc *UseCase) Calendars(ctx context.Context, p domain.Principal) ([]domain.Calendar, error) {
	cals, err := uc.calendars.ListCalendars(ctx, p)
	if err != nil || len(cals) > 0 {
		return cals, err
	}
	_, err = uc.calendars.CreateCalendar(ctx, p, domain.Calendar{
		ID: uuid.New(), TenantID: p.TenantID, MailboxID: p.MailboxID,
		Slug: DefaultCalendarSlug, DisplayName: uc.cfg.DefaultCalendarName,
	}, uc.cfg.Calendar.MaxCalendarsPerMailbox)
	if err != nil && !errors.Is(err, domain.ErrAlreadyExists) {
		return nil, err
	}
	return uc.calendars.ListCalendars(ctx, p)
}

func (uc *UseCase) Calendar(ctx context.Context, p domain.Principal, slug string) (domain.Calendar, error) {
	if !domain.ValidSlug(slug) {
		return domain.Calendar{}, domain.ErrNotFound
	}
	return uc.calendars.GetCalendar(ctx, p, slug)
}

func (uc *UseCase) CreateCalendar(ctx context.Context, p domain.Principal, slug, displayName, description string) (domain.Calendar, error) {
	displayName, description, err := collectionNames(slug, displayName, description)
	if err != nil {
		return domain.Calendar{}, err
	}
	return uc.calendars.CreateCalendar(ctx, p, domain.Calendar{
		ID: uuid.New(), TenantID: p.TenantID, MailboxID: p.MailboxID,
		Slug: slug, DisplayName: displayName, Description: description,
	}, uc.cfg.Calendar.MaxCalendarsPerMailbox)
}

func (uc *UseCase) DeleteCalendar(ctx context.Context, p domain.Principal, slug string) error {
	if !domain.ValidSlug(slug) {
		return domain.ErrNotFound
	}
	return uc.calendars.DeleteCalendar(ctx, p, slug)
}

func (uc *UseCase) Events(ctx context.Context, p domain.Principal, slug string) (domain.Calendar, []domain.Event, error) {
	if !domain.ValidSlug(slug) {
		return domain.Calendar{}, nil, domain.ErrNotFound
	}
	return uc.calendars.ListEvents(ctx, p, slug, domain.EventWindow{})
}

func (uc *UseCase) Event(ctx context.Context, p domain.Principal, slug, resource string) (domain.Event, error) {
	if !domain.ValidSlug(slug) || !domain.ValidEventResourceName(resource) {
		return domain.Event{}, domain.ErrNotFound
	}
	return uc.calendars.GetEvent(ctx, p, slug, resource)
}

// EventsByName atiende calendar-multiget: los nombres que no son validos o no existen se omiten, y quien
// llama los informa como 404.
func (uc *UseCase) EventsByName(ctx context.Context, p domain.Principal, slug string, resources []string) ([]domain.Event, error) {
	if !domain.ValidSlug(slug) {
		return nil, domain.ErrNotFound
	}
	valid := make([]string, 0, len(resources))
	for _, r := range resources {
		if domain.ValidEventResourceName(r) {
			valid = append(valid, r)
		}
	}
	if len(valid) == 0 {
		if _, err := uc.calendars.GetCalendar(ctx, p, slug); err != nil {
			return nil, err
		}
		return nil, nil
	}
	return uc.calendars.GetEvents(ctx, p, slug, valid)
}

// QueryEvents atiende calendar-query. La base descarta por tiempo los eventos que no pueden tocar el
// rango (con sus indices) y aqui se decide con exactitud sobre los que quedan, expandiendo las
// recurrencias con un presupuesto acotado por evento y por consulta: lo que no se puede decidir se
// devuelve. Un evento guardado que ya no se pueda leer no aparece en el resultado.
func (uc *UseCase) QueryEvents(ctx context.Context, p domain.Principal, slug string, filter domain.CalendarFilter) (domain.Calendar, []domain.Event, error) {
	if !domain.ValidSlug(slug) {
		return domain.Calendar{}, nil, domain.ErrNotFound
	}
	cal, candidates, err := uc.calendars.ListEvents(ctx, p, slug, filter.Window())
	if err != nil {
		return cal, nil, err
	}
	budget := domain.NewBudget(uc.cfg.Calendar.MaxQueryWork)
	out := make([]domain.Event, 0, len(candidates))
	for _, e := range candidates {
		obj, err := domain.ParseStoredCalendarObject(e.ICal)
		if err != nil {
			uc.logger.Warn("mail-dav: evento guardado ilegible, se omite de la consulta",
				zap.String("event_id", e.ID.String()), zap.Error(err))
			continue
		}
		if filter.Matches(obj, budget, uc.cfg.Calendar.MaxRecurrenceWork) {
			out = append(out, e)
		}
	}
	return cal, out, nil
}

// PutEvent crea o reemplaza el evento. created distingue el 201 del 204.
func (uc *UseCase) PutEvent(ctx context.Context, p domain.Principal, slug, resource, raw string, cond domain.Precondition) (etag string, created bool, err error) {
	if !domain.ValidSlug(slug) {
		return "", false, domain.ErrNotFound
	}
	e, err := domain.NewEvent(resource, raw, uc.cfg.Calendar)
	if err != nil {
		return "", false, err
	}
	e.ID, e.TenantID, e.MailboxID = uuid.New(), p.TenantID, p.MailboxID
	created, err = uc.calendars.PutEvent(ctx, p, slug, e, cond, uc.cfg.Calendar.MaxEventsPerMailbox, uc.cfg.Limits.MaxChangesRetained)
	if err != nil {
		return "", false, err
	}
	return e.ETag, created, nil
}

func (uc *UseCase) DeleteEvent(ctx context.Context, p domain.Principal, slug, resource string, cond domain.Precondition) error {
	if !domain.ValidSlug(slug) || !domain.ValidEventResourceName(resource) {
		return domain.ErrNotFound
	}
	return uc.calendars.DeleteEvent(ctx, p, slug, resource, cond, uc.cfg.Limits.MaxChangesRetained)
}

// CalendarSyncResult es la respuesta de sync-collection de un calendario.
type CalendarSyncResult struct {
	Token   string
	Changed []domain.Event
	Removed []string
}

// SyncCalendar atiende sync-collection. Con token vacio devuelve todos los eventos y ninguna baja.
func (uc *UseCase) SyncCalendar(ctx context.Context, p domain.Principal, slug, token string) (CalendarSyncResult, error) {
	if !domain.ValidSlug(slug) {
		return CalendarSyncResult{}, domain.ErrNotFound
	}
	calID, seq, initial, err := domain.ParseSyncToken(token)
	if err != nil {
		return CalendarSyncResult{}, err
	}
	if initial {
		cal, events, err := uc.calendars.ListEvents(ctx, p, slug, domain.EventWindow{})
		if err != nil {
			return CalendarSyncResult{}, err
		}
		return CalendarSyncResult{Token: domain.SyncToken(cal.ID, cal.SyncSeq), Changed: events}, nil
	}
	cal, changed, removed, err := uc.calendars.EventChangesSince(ctx, p, slug, seq)
	if err != nil {
		return CalendarSyncResult{}, err
	}
	if cal.ID != calID {
		return CalendarSyncResult{}, domain.ErrInvalidSyncToken
	}
	return CalendarSyncResult{Token: domain.SyncToken(cal.ID, cal.SyncSeq), Changed: changed, Removed: removed}, nil
}
