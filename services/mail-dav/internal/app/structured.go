package app

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-dav/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// La API estructurada (contactos y eventos en JSON para el webmail) trabaja sobre la libreta y el calendario
// por defecto del buzon y con los mismos almacenes que CardDAV y CalDAV: lo que se crea por un lado aparece
// por el otro. El id de un contacto o un evento es el nombre de su recurso sin la extension.

const (
	vcardExt = ".vcf"
	icsExt   = ".ics"
	// maxRewriteAttempts acota los reintentos de una actualizacion sin If-Match que choca con otra escritura
	// entre la lectura y el guardado: sin condicion del cliente se vuelve a leer y a aplicar sobre lo nuevo.
	maxRewriteAttempts = 3
)

// ContactView es un contacto en la forma de la API estructurada.
type ContactView struct {
	ID        string
	ETag      string
	Fields    domain.ContactFields
	UpdatedAt time.Time
}

// EventView es un evento en la forma de la API estructurada.
type EventView struct {
	ID     string
	ETag   string
	Fields domain.EventFields
}

// OccurrenceView es una aparicion de un evento con el id del evento.
type OccurrenceView struct {
	ID string
	domain.Occurrence
}

// ImportSkip es una tarjeta de un fichero importado que no se guardo: su posicion (desde cero) y el motivo.
type ImportSkip struct {
	Index  int
	Reason string
}

// ImportResult cuenta lo que hizo una importacion: contactos nuevos, actualizados (mismo UID) y descartados.
type ImportResult struct {
	Imported int
	Updated  int
	Skipped  []ImportSkip
}

// BindMailbox liga el contexto a la empresa y al buzon que nombra un servicio de la plataforma (el webmail,
// que los toma de su sesion): la misma identidad acotada que una peticion DAV, de modo que las politicas de
// fila valen tambien aqui.
func (uc *UseCase) BindMailbox(ctx context.Context, tenantID, mailboxID uuid.UUID) (context.Context, domain.Principal, error) {
	if tenantID == uuid.Nil || mailboxID == uuid.Nil {
		return ctx, domain.Principal{}, domain.ErrInvalidMailbox
	}
	p := domain.Principal{TenantID: tenantID, MailboxID: mailboxID}
	bound, err := uc.tenant.Bind(ctx, p)
	if err != nil {
		return ctx, domain.Principal{}, err
	}
	return bound, p, nil
}

// pickDefault elige la coleccion por defecto: la de slug preferido o, si el buzon la borro por DAV, la primera.
func pickDefault(cols []domain.Addressbook, preferred string) (string, error) {
	if len(cols) == 0 {
		return "", domain.ErrNotFound
	}
	for _, c := range cols {
		if c.Slug == preferred {
			return c.Slug, nil
		}
	}
	return cols[0].Slug, nil
}

func (uc *UseCase) defaultAddressbook(ctx context.Context, p domain.Principal) (string, error) {
	books, err := uc.Addressbooks(ctx, p)
	if err != nil {
		return "", err
	}
	return pickDefault(books, DefaultSlug)
}

func (uc *UseCase) defaultCalendar(ctx context.Context, p domain.Principal) (string, error) {
	cals, err := uc.Calendars(ctx, p)
	if err != nil {
		return "", err
	}
	return pickDefault(cals, DefaultCalendarSlug)
}

func contactResource(id string) (string, bool) {
	r := id + vcardExt
	return r, domain.ValidResourceName(r)
}

func eventResource(id string) (string, bool) {
	r := id + icsExt
	return r, domain.ValidEventResourceName(r)
}

func contactView(c domain.Contact) (ContactView, error) {
	f, err := domain.ContactFieldsOf(c.VCard)
	if err != nil {
		return ContactView{}, err
	}
	return ContactView{ID: strings.TrimSuffix(c.ResourceName, vcardExt), ETag: c.ETag, Fields: f, UpdatedAt: c.UpdatedAt}, nil
}

func eventView(e domain.Event) (EventView, error) {
	f, err := domain.EventFieldsOf(e.ICal)
	if err != nil {
		return EventView{}, err
	}
	return EventView{ID: strings.TrimSuffix(e.ResourceName, icsExt), ETag: e.ETag, Fields: f}, nil
}

// rewrite aplica una actualizacion leida y escrita con el etag leido. Si el cliente no puso condicion y otra
// escritura se cruza, se reintenta sobre lo nuevo; con condicion del cliente el choque es suyo (412).
func rewrite(clientCond bool, attempt func() error) error {
	for i := 0; ; i++ {
		err := attempt()
		if clientCond || i == maxRewriteAttempts-1 || !errors.Is(err, domain.ErrPreconditionFailed) {
			return err
		}
	}
}

func hasIfMatch(cond domain.Precondition) bool { return cond.IfMatchAny || len(cond.IfMatch) > 0 }

// ContactPage lista los contactos de la libreta por defecto que contienen q (sin distinguir mayusculas) en el
// nombre o en un correo, ordenados por nombre, y devuelve la pagina pedida con el total de coincidencias. La
// busqueda recorre los campos indexados, sin leer los vCard; solo se leen los de la pagina.
func (uc *UseCase) ContactPage(ctx context.Context, p domain.Principal, q string, page, perPage int) ([]ContactView, int, error) {
	slug, err := uc.defaultAddressbook(ctx, p)
	if err != nil {
		return nil, 0, err
	}
	_, all, err := uc.store.ListContacts(ctx, p, slug, uc.cfg.Limits.Read(false))
	if err != nil {
		return nil, 0, err
	}
	q = strings.ToLower(strings.TrimSpace(q))
	matches := all[:0]
	for _, c := range all {
		if q == "" || matchesContact(c, q) {
			matches = append(matches, c)
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		a, b := strings.ToLower(matches[i].DisplayName), strings.ToLower(matches[j].DisplayName)
		if a != b {
			return a < b
		}
		return matches[i].ResourceName < matches[j].ResourceName
	})
	total := len(matches)
	from := (page - 1) * perPage
	if from >= total {
		return []ContactView{}, total, nil
	}
	pageItems := matches[from:min(from+perPage, total)]
	names := make([]string, len(pageItems))
	for i, c := range pageItems {
		names[i] = c.ResourceName
	}
	loaded, err := uc.store.GetContacts(ctx, p, slug, names, uc.cfg.Limits.Read(true))
	if err != nil {
		return nil, 0, err
	}
	byName := make(map[string]domain.Contact, len(loaded))
	for _, c := range loaded {
		byName[c.ResourceName] = c
	}
	out := make([]ContactView, 0, len(names))
	for _, name := range names {
		c, ok := byName[name]
		if !ok {
			continue
		}
		v, err := contactView(c)
		if err != nil {
			uc.logger.Warn("mail-dav: vCard guardado ilegible, se omite del listado", zap.String("contact_id", c.ID.String()), zap.Error(err))
			continue
		}
		out = append(out, v)
	}
	return out, total, nil
}

func matchesContact(c domain.Contact, q string) bool {
	if strings.Contains(strings.ToLower(c.DisplayName), q) {
		return true
	}
	for _, e := range c.Emails {
		if strings.Contains(strings.ToLower(e), q) {
			return true
		}
	}
	return false
}

// ContactByID lee un contacto de la libreta por defecto.
func (uc *UseCase) ContactByID(ctx context.Context, p domain.Principal, id string) (ContactView, error) {
	resource, ok := contactResource(id)
	if !ok {
		return ContactView{}, domain.ErrNotFound
	}
	slug, err := uc.defaultAddressbook(ctx, p)
	if err != nil {
		return ContactView{}, err
	}
	c, err := uc.store.GetContact(ctx, p, slug, resource)
	if err != nil {
		return ContactView{}, err
	}
	return contactView(c)
}

func (uc *UseCase) saveContact(ctx context.Context, p domain.Principal, slug, resource, raw string, cond domain.Precondition) (ContactView, error) {
	c, err := domain.NewContact(resource, raw, uc.cfg.Limits)
	if err != nil {
		return ContactView{}, err
	}
	c.ID, c.TenantID, c.MailboxID = uuid.New(), p.TenantID, p.MailboxID
	if _, err := uc.store.PutContact(ctx, p, slug, c, cond, uc.cfg.Limits.Write(uc.cfg.Limits.MaxContactsPerMailbox)); err != nil {
		return ContactView{}, err
	}
	c.UpdatedAt = uc.now()
	return contactView(c)
}

// CreateContact crea un contacto en la libreta por defecto con un UUID nuevo como UID y como id.
func (uc *UseCase) CreateContact(ctx context.Context, p domain.Principal, in domain.ContactFields) (ContactView, error) {
	f, err := in.Normalize()
	if err != nil {
		return ContactView{}, err
	}
	slug, err := uc.defaultAddressbook(ctx, p)
	if err != nil {
		return ContactView{}, err
	}
	id := uuid.NewString()
	raw, err := domain.BuildVCard("", id, f, uc.now())
	if err != nil {
		return ContactView{}, err
	}
	return uc.saveContact(ctx, p, slug, id+vcardExt, raw, domain.Precondition{IfNoneMatchAny: true})
}

// UpdateContact reemplaza los campos del contacto conservando lo que la API no expresa (ver BuildVCard). cond
// es el If-Match del cliente, opcional.
func (uc *UseCase) UpdateContact(ctx context.Context, p domain.Principal, id string, in domain.ContactFields, cond domain.Precondition) (ContactView, error) {
	resource, ok := contactResource(id)
	if !ok {
		return ContactView{}, domain.ErrNotFound
	}
	f, err := in.Normalize()
	if err != nil {
		return ContactView{}, err
	}
	slug, err := uc.defaultAddressbook(ctx, p)
	if err != nil {
		return ContactView{}, err
	}
	var out ContactView
	err = rewrite(hasIfMatch(cond), func() error {
		cur, err := uc.store.GetContact(ctx, p, slug, resource)
		if err != nil {
			return err
		}
		if err := cond.Check(&cur.ETag); err != nil {
			return err
		}
		raw, err := domain.BuildVCard(cur.VCard, "", f, uc.now())
		if err != nil {
			return err
		}
		out, err = uc.saveContact(ctx, p, slug, resource, raw, domain.Precondition{IfMatch: []string{cur.ETag}})
		return err
	})
	return out, err
}

// DeleteContactByID borra un contacto de la libreta por defecto.
func (uc *UseCase) DeleteContactByID(ctx context.Context, p domain.Principal, id string, cond domain.Precondition) error {
	resource, ok := contactResource(id)
	if !ok {
		return domain.ErrNotFound
	}
	slug, err := uc.defaultAddressbook(ctx, p)
	if err != nil {
		return err
	}
	return uc.store.DeleteContact(ctx, p, slug, resource, cond, uc.cfg.Limits.MaxChangesRetained)
}

// ExportContacts recorre los vCard de la libreta por defecto, de uno en uno y sin acumularlos.
func (uc *UseCase) ExportContacts(ctx context.Context, p domain.Principal, write func(vcard string) error) error {
	slug, err := uc.defaultAddressbook(ctx, p)
	if err != nil {
		return err
	}
	_, err = uc.store.EachContact(ctx, p, slug, func(c domain.Contact) (bool, error) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		card := c.VCard
		if !strings.HasSuffix(card, "\n") {
			card += "\r\n"
		}
		return true, write(card)
	})
	return err
}

// importSkippable son los errores que descartan una tarjeta sin abortar la importacion.
func importSkippable(err error) (string, bool) {
	var bad *domain.VCardError
	var conflict *domain.UIDConflictError
	switch {
	case errors.As(err, &bad):
		return bad.Reason, true
	case errors.As(err, &conflict):
		return "el UID ya lo usa otro contacto", true
	case errors.Is(err, domain.ErrContactLimit), errors.Is(err, domain.ErrStorageLimit), errors.Is(err, domain.ErrInvalidName):
		return err.Error(), true
	}
	return "", false
}

// ImportContacts guarda en la libreta por defecto las tarjetas de un fichero .vcf: la que trae un UID que ya
// existe reemplaza a ese contacto y la que no, se crea (con un UID nuevo si no traia). Cada tarjeta se valida
// por separado y la que no se puede guardar se informa en Skipped; un fallo de la base aborta la importacion,
// y lo guardado hasta entonces queda.
func (uc *UseCase) ImportContacts(ctx context.Context, p domain.Principal, raw string, maxCards int) (ImportResult, error) {
	cards, err := domain.SplitVCards(raw, maxCards)
	if err != nil {
		return ImportResult{}, err
	}
	slug, err := uc.defaultAddressbook(ctx, p)
	if err != nil {
		return ImportResult{}, err
	}
	_, existing, err := uc.store.ListContacts(ctx, p, slug, uc.cfg.Limits.Read(false))
	if err != nil {
		return ImportResult{}, err
	}
	byUID := make(map[string]string, len(existing))
	for _, c := range existing {
		byUID[c.UID] = c.ResourceName
	}
	res := ImportResult{Skipped: []ImportSkip{}}
	for i, card := range cards {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		card = domain.EnsureUID(card, uuid.NewString())
		parsed, err := domain.ParseVCard(card, uc.cfg.Limits)
		if err != nil {
			reason, _ := importSkippable(err)
			res.Skipped = append(res.Skipped, ImportSkip{Index: i, Reason: reason})
			continue
		}
		resource, update := byUID[parsed.UID]
		if !update {
			resource = uuid.NewString() + vcardExt
		}
		if _, err := uc.saveContact(ctx, p, slug, resource, card, domain.Precondition{}); err != nil {
			if reason, ok := importSkippable(err); ok {
				res.Skipped = append(res.Skipped, ImportSkip{Index: i, Reason: reason})
				continue
			}
			return res, err
		}
		byUID[parsed.UID] = resource
		if update {
			res.Updated++
		} else {
			res.Imported++
		}
	}
	return res, nil
}

// EventOccurrences devuelve las apariciones de los eventos del calendario por defecto en [start, end), ordenadas
// por inicio. La expansion gasta el mismo presupuesto por evento y por consulta que calendar-query; pasar de
// maxOccurrences es domain.ErrResultTooLarge.
func (uc *UseCase) EventOccurrences(ctx context.Context, p domain.Principal, start, end time.Time, maxOccurrences int) ([]OccurrenceView, error) {
	uc.rememberAddress(ctx, p)
	slug, err := uc.defaultCalendar(ctx, p)
	if err != nil {
		return nil, err
	}
	budget := domain.NewBudget(uc.cfg.Calendar.MaxQueryWork)
	rng := domain.TimeRange{Start: &start, End: &end}
	out := []OccurrenceView{}
	_, err = uc.calendars.EachEvent(ctx, p, slug, domain.EventWindow{Start: &start, End: &end}, func(e domain.Event) (bool, error) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		obj, err := domain.ParseStoredCalendarObject(e.ICal)
		if err != nil {
			uc.logger.Warn("mail-dav: evento guardado ilegible, se omite del calendario", zap.String("event_id", e.ID.String()), zap.Error(err))
			return true, nil
		}
		release := budget.Cap(uc.cfg.Calendar.MaxRecurrenceWork)
		occs := obj.Occurrences(rng, budget)
		release()
		id := strings.TrimSuffix(e.ResourceName, icsExt)
		for _, o := range occs {
			if len(out) >= maxOccurrences {
				return false, domain.ErrResultTooLarge
			}
			out = append(out, OccurrenceView{ID: id, Occurrence: o})
		}
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Start.Equal(out[j].Start) {
			return out[i].Start.Before(out[j].Start)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// EventByID lee un evento del calendario por defecto.
func (uc *UseCase) EventByID(ctx context.Context, p domain.Principal, id string) (EventView, error) {
	resource, ok := eventResource(id)
	if !ok {
		return EventView{}, domain.ErrNotFound
	}
	slug, err := uc.defaultCalendar(ctx, p)
	if err != nil {
		return EventView{}, err
	}
	e, err := uc.calendars.GetEvent(ctx, p, slug, resource)
	if err != nil {
		return EventView{}, err
	}
	return eventView(e)
}

func (uc *UseCase) saveEvent(ctx context.Context, p domain.Principal, slug, resource, raw string, cond domain.Precondition) (EventView, error) {
	uc.rememberAddress(ctx, p)
	e, err := uc.newEvent(resource, raw)
	if err != nil {
		return EventView{}, err
	}
	e.ID, e.TenantID, e.MailboxID = uuid.New(), p.TenantID, p.MailboxID
	if _, err := uc.calendars.PutEvent(ctx, p, slug, e, cond, uc.cfg.Limits.Write(uc.cfg.Calendar.MaxEventsPerMailbox)); err != nil {
		return EventView{}, err
	}
	return eventView(e)
}

// CreateEvent crea un evento en el calendario por defecto con un UUID nuevo como UID y como id.
func (uc *UseCase) CreateEvent(ctx context.Context, p domain.Principal, in domain.EventFields) (EventView, error) {
	f, err := in.Normalize()
	if err != nil {
		return EventView{}, err
	}
	slug, err := uc.defaultCalendar(ctx, p)
	if err != nil {
		return EventView{}, err
	}
	id := uuid.NewString()
	raw, err := domain.BuildCalendarObject("", id, f, uc.now())
	if err != nil {
		return EventView{}, err
	}
	return uc.saveEvent(ctx, p, slug, id+icsExt, raw, domain.Precondition{IfNoneMatchAny: true})
}

// UpdateEvent reemplaza los campos de la serie conservando lo que la API no expresa (ver
// BuildCalendarObject). cond es el If-Match del cliente, opcional.
func (uc *UseCase) UpdateEvent(ctx context.Context, p domain.Principal, id string, in domain.EventFields, cond domain.Precondition) (EventView, error) {
	resource, ok := eventResource(id)
	if !ok {
		return EventView{}, domain.ErrNotFound
	}
	f, err := in.Normalize()
	if err != nil {
		return EventView{}, err
	}
	slug, err := uc.defaultCalendar(ctx, p)
	if err != nil {
		return EventView{}, err
	}
	var out EventView
	err = rewrite(hasIfMatch(cond), func() error {
		cur, err := uc.calendars.GetEvent(ctx, p, slug, resource)
		if err != nil {
			return err
		}
		if err := cond.Check(&cur.ETag); err != nil {
			return err
		}
		raw, err := domain.BuildCalendarObject(cur.ICal, "", f, uc.now())
		if err != nil {
			return err
		}
		out, err = uc.saveEvent(ctx, p, slug, resource, raw, domain.Precondition{IfMatch: []string{cur.ETag}})
		return err
	})
	return out, err
}

// DeleteEventByID borra un evento (la serie entera) del calendario por defecto.
func (uc *UseCase) DeleteEventByID(ctx context.Context, p domain.Principal, id string, cond domain.Precondition) error {
	resource, ok := eventResource(id)
	if !ok {
		return domain.ErrNotFound
	}
	slug, err := uc.defaultCalendar(ctx, p)
	if err != nil {
		return err
	}
	return uc.calendars.DeleteEvent(ctx, p, slug, resource, cond, uc.cfg.Limits.MaxChangesRetained)
}
