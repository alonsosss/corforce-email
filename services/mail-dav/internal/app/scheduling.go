package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-dav/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// SchedulingConfig rige la planificacion: cuanto de la ocupacion de cada evento se materializa al guardarlo
// (desde BusyLookback atras hasta BusyHorizon adelante, como mucho MaxBusyPerEvent tramos), cuantos eventos
// pendientes completa una consulta de disponibilidad (BusyRefreshMax) y los topes de la disponibilidad y de las
// citas. BusyHorizon a cero apaga todo: los eventos se guardan como hasta ahora y la disponibilidad, las
// invitaciones y las citas no estan.
type SchedulingConfig struct {
	BusyHorizon              time.Duration
	BusyLookback             time.Duration
	MaxBusyPerEvent          int
	BusyRefreshMax           int
	MaxAvailabilityAddresses int
	BookingMaxDaily          int
	BookingMaxPerVisitor     int
	BookingMaxSlots          int
	BookingMaxWindow         time.Duration
}

func (c SchedulingConfig) enabled() bool { return c.BusyHorizon > 0 }

func (c SchedulingConfig) Validate() error {
	if !c.enabled() {
		return nil
	}
	for name, v := range map[string]int{
		"MaxBusyPerEvent": c.MaxBusyPerEvent, "BusyRefreshMax": c.BusyRefreshMax, "MaxAvailabilityAddresses": c.MaxAvailabilityAddresses,
		"BookingMaxDaily": c.BookingMaxDaily, "BookingMaxPerVisitor": c.BookingMaxPerVisitor, "BookingMaxSlots": c.BookingMaxSlots,
	} {
		if v < 1 {
			return fmt.Errorf("%s debe ser mayor que cero", name)
		}
	}
	if c.MaxAvailabilityAddresses > domain.MaxBusyMailboxes {
		return fmt.Errorf("MaxAvailabilityAddresses no puede pasar de %d", domain.MaxBusyMailboxes)
	}
	if c.BusyLookback <= 0 || c.BookingMaxWindow <= 0 || c.BookingMaxWindow > domain.MaxBusyWindow {
		return errors.New("BusyLookback y BookingMaxWindow deben ser positivos y la ventana de citas no puede pasar de la de ocupación")
	}
	return nil
}

// ErrSchedulingDisabled: la planificacion no esta configurada en este despliegue.
var ErrSchedulingDisabled = fmt.Errorf("%w: planificación no configurada", domain.ErrUnavailable)

func (uc *UseCase) schedulingStore() error {
	if uc.scheduling == nil || !uc.cfg.Scheduling.enabled() {
		return ErrSchedulingDisabled
	}
	return nil
}

// busyPlan es lo que se materializa de la ocupacion de un evento al guardarlo ahora.
func (uc *UseCase) busyPlan() domain.BusyPlan {
	c := uc.cfg.Scheduling
	if !c.enabled() || uc.scheduling == nil {
		return domain.BusyPlan{}
	}
	now := uc.now().UTC()
	return domain.BusyPlan{From: now.Add(-c.BusyLookback), To: now.Add(c.BusyHorizon), MaxIntervals: c.MaxBusyPerEvent}
}

// newEvent valida el iCalendar y materializa su ocupacion (ver domain.NewEventAt).
func (uc *UseCase) newEvent(resource, raw string) (domain.Event, error) {
	return domain.NewEventAt(resource, raw, uc.cfg.Calendar, uc.busyPlan())
}

// maxRememberedAddresses acota la cache de direcciones ya registradas.
const maxRememberedAddresses = 50000

// addressCache recuerda que direccion se registro ya de cada buzon, para no escribirla en cada peticion.
type addressCache struct {
	mu    sync.Mutex
	known map[string]string
}

func newAddressCache() *addressCache { return &addressCache{known: map[string]string{}} }

func cacheKey(p domain.Principal) string { return p.TenantID.String() + "/" + p.MailboxID.String() }

func (c *addressCache) has(p domain.Principal, address string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.known[cacheKey(p)] == address
}

func (c *addressCache) put(p domain.Principal, address string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.known) >= maxRememberedAddresses {
		c.known = map[string]string{}
	}
	c.known[cacheKey(p)] = address
}

func (c *addressCache) forget(p domain.Principal) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.known, cacheKey(p))
}

// rememberAddress registra la direccion del buzon (la de mail-auth en DAV, la de la sesion en el webmail) para que
// sus companeros puedan pedir su disponibilidad. No falla la peticion: sin registro, el buzon aparece sin datos.
func (uc *UseCase) rememberAddress(ctx context.Context, p domain.Principal) {
	if uc.scheduling == nil || !uc.cfg.Scheduling.enabled() || p.Username == "" {
		return
	}
	address, err := domain.NormalizeAddress("address", p.Username)
	if err != nil || uc.addresses.has(p, address) {
		return
	}
	if err := uc.scheduling.RegisterAddress(ctx, p, address); err != nil {
		uc.logger.Warn("mail-dav: no se pudo registrar la direccion del buzon", zap.String("mailbox_id", p.MailboxID.String()), zap.Error(err))
		return
	}
	uc.addresses.put(p, address)
}

// completeBusy materializa la ocupacion de los eventos de esos buzones que no la tienen hasta until: los
// guardados antes de la planificacion y las series que pasaron su horizonte. Cada buzon se trabaja con su propia
// identidad, como una peticion suya; nada de su contenido sale de aqui. Devuelve partial si quedaron pendientes
// por encima de BusyRefreshMax.
func (uc *UseCase) completeBusy(ctx context.Context, p domain.Principal, mailboxes []uuid.UUID, until time.Time) (bool, error) {
	limit := uc.cfg.Scheduling.BusyRefreshMax
	pending, err := uc.scheduling.PendingBusy(ctx, p, mailboxes, until, limit+1)
	if err != nil {
		return false, err
	}
	total := 0
	for _, ids := range pending {
		total += len(ids)
	}
	partial := total > limit
	plan := uc.busyPlan()
	if plan.To.Before(until) {
		plan.To = until
	}
	done := 0
	for mailbox, ids := range pending {
		if done >= limit {
			break
		}
		if len(ids) > limit-done {
			ids = ids[:limit-done]
		}
		done += len(ids)
		bound, owner, err := uc.BindMailbox(ctx, p.TenantID, mailbox)
		if err != nil {
			return partial, err
		}
		events, err := uc.scheduling.EventsByID(bound, owner, ids)
		if err != nil {
			return partial, err
		}
		for _, e := range events {
			if err := ctx.Err(); err != nil {
				return partial, err
			}
			var busy []domain.Interval
			var horizon *time.Time
			if obj, err := domain.ParseStoredCalendarObject(e.ICal); err != nil {
				uc.logger.Warn("mail-dav: evento guardado ilegible, se da por libre", zap.String("event_id", e.ID.String()), zap.Error(err))
			} else {
				busy, horizon = obj.Busy(plan, e.LastEnd, domain.NewBudget(uc.cfg.Calendar.MaxRecurrenceWork))
			}
			if _, err := uc.scheduling.ReplaceBusy(bound, owner, e.ID, e.ETag, busy, horizon); err != nil {
				return partial, err
			}
		}
	}
	return partial, nil
}

// MailboxAvailability es la ocupacion de un buzon de la empresa: Known falso si la direccion no es de un buzon que
// haya usado el calendario (no hay nada que decir de el); Partial si parte de sus eventos no se pudo completar en
// esta consulta.
type MailboxAvailability struct {
	Address string
	Known   bool
	Partial bool
	Busy    []domain.Interval
}

func (uc *UseCase) checkBusyWindow(from, to time.Time) error {
	now := uc.now().UTC()
	switch {
	case !to.After(from):
		return &domain.FieldError{Field: "end", Reason: "debe ser posterior al inicio"}
	case to.Sub(from) > domain.MaxBusyWindow:
		return &domain.FieldError{Field: "end", Reason: "la ventana supera el máximo de días"}
	case from.Before(now.Add(-uc.cfg.Scheduling.BusyLookback)):
		return &domain.FieldError{Field: "start", Reason: "es anterior a la ocupación que se conserva"}
	case to.After(now.Add(uc.cfg.Scheduling.BusyHorizon)):
		return &domain.FieldError{Field: "end", Reason: "supera el horizonte de la ocupación"}
	}
	return nil
}

// Availability devuelve la ocupacion (solo inicio y fin) de buzones de la misma empresa en [from, to), por su
// direccion. La empresa es la de la sesion; una direccion de otra empresa no esta en esta base y sale como
// desconocida.
func (uc *UseCase) Availability(ctx context.Context, p domain.Principal, addresses []string, from, to time.Time) ([]MailboxAvailability, error) {
	if err := uc.schedulingStore(); err != nil {
		return nil, err
	}
	if len(addresses) == 0 {
		return nil, &domain.FieldError{Field: "addresses", Reason: "es obligatorio"}
	}
	if len(addresses) > uc.cfg.Scheduling.MaxAvailabilityAddresses {
		return nil, &domain.FieldError{Field: "addresses", Reason: "demasiadas direcciones"}
	}
	if err := uc.checkBusyWindow(from, to); err != nil {
		return nil, err
	}
	uc.rememberAddress(ctx, p)
	normalized := make([]string, 0, len(addresses))
	seen := map[string]bool{}
	for i, a := range addresses {
		n, err := domain.NormalizeAddress("addresses["+strconv.Itoa(i)+"]", a)
		if err != nil {
			return nil, err
		}
		if !seen[n] {
			seen[n] = true
			normalized = append(normalized, n)
		}
	}
	byAddress, err := uc.scheduling.ResolveMailboxes(ctx, p, normalized)
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, 0, len(byAddress))
	for _, id := range byAddress {
		ids = append(ids, id)
	}
	partial := false
	busy := map[uuid.UUID][]domain.Interval{}
	if len(ids) > 0 {
		if partial, err = uc.completeBusy(ctx, p, ids, to); err != nil {
			return nil, err
		}
		if busy, err = uc.scheduling.BusyIntervals(ctx, p, ids, from, to); err != nil {
			return nil, err
		}
	}
	out := make([]MailboxAvailability, 0, len(normalized))
	for _, a := range normalized {
		id, known := byAddress[a]
		item := MailboxAvailability{Address: a, Known: known, Busy: []domain.Interval{}}
		if known {
			item.Busy, item.Partial = domain.MergeIntervals(busy[id]), partial
		}
		out = append(out, item)
	}
	return out, nil
}

// newPublicID son 144 bits aleatorios en base64 para URL: el enlace de una pagina de citas no se puede adivinar.
func newPublicID() (string, error) {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// BookingSettings devuelve la pagina de citas del buzon; domain.ErrNotFound si aun no la configuro.
func (uc *UseCase) BookingSettings(ctx context.Context, p domain.Principal) (domain.BookingPage, error) {
	if err := uc.schedulingStore(); err != nil {
		return domain.BookingPage{}, err
	}
	return uc.scheduling.BookingPage(ctx, p)
}

// SaveBookingSettings crea o reemplaza la pagina de citas del buzon. El dueno (direccion y nombre) es el de la
// sesion que la guarda; regenerate cambia el enlace (el anterior deja de funcionar).
func (uc *UseCase) SaveBookingSettings(ctx context.Context, p domain.Principal, in domain.BookingSettings, ownerName string, regenerate bool) (domain.BookingPage, error) {
	if err := uc.schedulingStore(); err != nil {
		return domain.BookingPage{}, err
	}
	owner, err := domain.NormalizeAddress("owner", p.Username)
	if err != nil {
		return domain.BookingPage{}, &domain.FieldError{Field: "owner", Reason: "falta la dirección del buzón"}
	}
	settings, err := in.Normalize(uc.cfg.Scheduling.BookingMaxDaily)
	if err != nil {
		return domain.BookingPage{}, err
	}
	name, err := domain.NormalizePartyName("owner_name", ownerName)
	if err != nil {
		return domain.BookingPage{}, err
	}
	page, err := uc.scheduling.BookingPage(ctx, p)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		page = domain.BookingPage{ID: uuid.New()}
	case err != nil:
		return domain.BookingPage{}, err
	}
	if page.PublicID == "" || regenerate {
		if page.PublicID, err = newPublicID(); err != nil {
			return domain.BookingPage{}, err
		}
	}
	page.BookingSettings, page.OwnerAddress, page.OwnerName = settings, owner, name
	uc.rememberAddress(ctx, p)
	return uc.scheduling.SaveBookingPage(ctx, p, page)
}

// publicPage resuelve la pagina activa de un enlace publico: la empresa del enlace elige la base, la funcion de la
// migracion da el buzon dueno y desde ahi se trabaja con la identidad de ese buzon. Todo lo que no lleva a una
// pagina activa es domain.ErrNotFound, sin distinguir la causa.
func (uc *UseCase) publicPage(ctx context.Context, tenantID uuid.UUID, publicID string) (context.Context, domain.Principal, domain.BookingPage, error) {
	if err := uc.schedulingStore(); err != nil {
		return ctx, domain.Principal{}, domain.BookingPage{}, err
	}
	if tenantID == uuid.Nil || !domain.ValidPublicID(publicID) {
		return ctx, domain.Principal{}, domain.BookingPage{}, domain.ErrNotFound
	}
	tp := domain.Principal{TenantID: tenantID}
	tctx, err := uc.tenant.Bind(ctx, tp)
	if err != nil {
		if errors.Is(err, domain.ErrTenantUnknown) {
			return ctx, domain.Principal{}, domain.BookingPage{}, domain.ErrNotFound
		}
		return ctx, domain.Principal{}, domain.BookingPage{}, err
	}
	owner, err := uc.scheduling.BookingPageOwner(tctx, tp, publicID)
	if err != nil {
		return ctx, domain.Principal{}, domain.BookingPage{}, err
	}
	if owner == uuid.Nil {
		return ctx, domain.Principal{}, domain.BookingPage{}, domain.ErrNotFound
	}
	octx, op, err := uc.BindMailbox(ctx, tenantID, owner)
	if err != nil {
		return ctx, domain.Principal{}, domain.BookingPage{}, err
	}
	page, err := uc.scheduling.BookingPage(octx, op)
	if err != nil {
		return ctx, domain.Principal{}, domain.BookingPage{}, err
	}
	if !page.Active || page.PublicID != publicID {
		return ctx, domain.Principal{}, domain.BookingPage{}, domain.ErrNotFound
	}
	op.Username = page.OwnerAddress
	return octx, op, page, nil
}

// ownerBusy es la ocupacion del dueno de una pagina en [from, to), completando antes sus eventos pendientes.
func (uc *UseCase) ownerBusy(ctx context.Context, owner domain.Principal, from, to time.Time) ([]domain.Interval, error) {
	mailboxes := []uuid.UUID{owner.MailboxID}
	if _, err := uc.completeBusy(ctx, owner, mailboxes, to); err != nil {
		return nil, err
	}
	busy, err := uc.scheduling.BusyIntervals(ctx, owner, mailboxes, from, to)
	if err != nil {
		return nil, err
	}
	return busy[owner.MailboxID], nil
}

// PublicBooking es lo que ve el visitante de una pagina de citas: su configuracion y los huecos libres.
type PublicBooking struct {
	Page  domain.BookingPage
	Slots []domain.Interval
}

// PublicBookingSlots devuelve la pagina activa de un enlace publico con sus huecos libres en [from, to).
func (uc *UseCase) PublicBookingSlots(ctx context.Context, tenantID uuid.UUID, publicID string, from, to time.Time) (PublicBooking, error) {
	octx, owner, page, err := uc.publicPage(ctx, tenantID, publicID)
	if err != nil {
		return PublicBooking{}, err
	}
	if !to.After(from) {
		return PublicBooking{}, &domain.FieldError{Field: "end", Reason: "debe ser posterior al inicio"}
	}
	if to.Sub(from) > uc.cfg.Scheduling.BookingMaxWindow {
		return PublicBooking{}, &domain.FieldError{Field: "end", Reason: "la ventana supera el máximo de días"}
	}
	now := uc.now().UTC()
	if earliest := now.Add(time.Duration(page.MinNoticeMinutes) * time.Minute); from.Before(earliest) {
		from = earliest
	}
	if latest := now.AddDate(0, 0, page.MaxAdvanceDays); to.After(latest) {
		to = latest
	}
	if !to.After(from) {
		return PublicBooking{Page: page, Slots: []domain.Interval{}}, nil
	}
	margin := time.Duration(page.BufferMinutes) * time.Minute
	busy, err := uc.ownerBusy(octx, owner, from.Add(-margin), to.Add(margin))
	if err != nil {
		return PublicBooking{}, err
	}
	return PublicBooking{Page: page, Slots: page.Slots(from, to, now, busy, uc.cfg.Scheduling.BookingMaxSlots)}, nil
}

// BookingResult es una cita reservada: la pagina (con su dueno), el evento creado en el calendario del dueno y la
// invitacion (REQUEST, sin la nota del visitante) que el webmail envia desde el buzon del dueno.
type BookingResult struct {
	Page       domain.BookingPage
	Event      EventView
	Invitation domain.Outgoing
}

func visitorHash(pageID uuid.UUID, email string) string {
	sum := sha256.Sum256([]byte(pageID.String() + ":" + email))
	return hex.EncodeToString(sum[:])
}

// Book reserva un hueco de una pagina publica: comprueba que sea un hueco libre, crea el evento en el calendario
// por defecto del dueno (con el dueno como organizador y el visitante invitado) y anota la reserva con sus topes,
// todo en una transaccion con el cerrojo del buzon dueno.
func (uc *UseCase) Book(ctx context.Context, tenantID uuid.UUID, publicID string, in domain.BookingRequest) (BookingResult, error) {
	req, err := in.Normalize()
	if err != nil {
		return BookingResult{}, err
	}
	octx, owner, page, err := uc.publicPage(ctx, tenantID, publicID)
	if err != nil {
		return BookingResult{}, err
	}
	if req.Email == page.OwnerAddress {
		return BookingResult{}, &domain.FieldError{Field: "email", Reason: "no puede ser la dirección del dueño de la página"}
	}
	now := uc.now().UTC()
	dur := time.Duration(page.DurationMinutes) * time.Minute
	margin := time.Duration(page.BufferMinutes) * time.Minute
	busy, err := uc.ownerBusy(octx, owner, req.Start.Add(-margin), req.Start.Add(dur+margin))
	if err != nil {
		return BookingResult{}, err
	}
	if !page.SlotAvailable(req.Start, now, busy) {
		return BookingResult{}, domain.ErrSlotUnavailable
	}
	fields, err := page.BookingEventFields(req).Normalize()
	if err != nil {
		return BookingResult{}, err
	}
	slug, err := uc.defaultCalendar(octx, owner)
	if err != nil {
		return BookingResult{}, err
	}
	id := uuid.NewString()
	raw, err := domain.BuildCalendarObject("", id, fields, now)
	if err != nil {
		return BookingResult{}, err
	}
	e, err := uc.newEvent(id+icsExt, raw)
	if err != nil {
		return BookingResult{}, err
	}
	e.ID, e.TenantID, e.MailboxID = uuid.New(), owner.TenantID, owner.MailboxID
	err = uc.scheduling.ReserveBooking(octx, owner, slug, e, domain.Booking{
		PageID: page.ID, EventResource: e.ResourceName, Start: req.Start, End: req.Start.Add(dur), VisitorHash: visitorHash(page.ID, req.Email),
	}, domain.BookingLimits{
		Daily: page.DailyLimit, PerVisitor: uc.cfg.Scheduling.BookingMaxPerVisitor, Buffer: margin,
		Write: uc.cfg.Limits.Write(uc.cfg.Calendar.MaxEventsPerMailbox),
	})
	if err != nil {
		return BookingResult{}, err
	}
	view, err := eventView(e)
	if err != nil {
		return BookingResult{}, err
	}
	invitation, err := domain.BuildInvitation(raw, domain.MethodRequest, now, true)
	if err != nil {
		return BookingResult{}, err
	}
	uc.logger.Info("mail-dav: cita reservada", zap.String("tenant_id", owner.TenantID.String()), zap.String("mailbox_id", owner.MailboxID.String()),
		zap.String("event_id", view.ID), zap.Time("start", req.Start))
	return BookingResult{Page: page, Event: view, Invitation: invitation}, nil
}

// EventInvitation escribe la invitacion (REQUEST o CANCEL) de un evento del calendario por defecto, para que el
// webmail la envie desde el buzon del organizador.
func (uc *UseCase) EventInvitation(ctx context.Context, p domain.Principal, id, method string) (domain.Outgoing, EventView, error) {
	resource, ok := eventResource(id)
	if !ok {
		return domain.Outgoing{}, EventView{}, domain.ErrNotFound
	}
	slug, err := uc.defaultCalendar(ctx, p)
	if err != nil {
		return domain.Outgoing{}, EventView{}, err
	}
	e, err := uc.calendars.GetEvent(ctx, p, slug, resource)
	if err != nil {
		return domain.Outgoing{}, EventView{}, err
	}
	view, err := eventView(e)
	if err != nil {
		return domain.Outgoing{}, EventView{}, err
	}
	out, err := domain.BuildInvitation(e.ICal, method, uc.now(), false)
	return out, view, err
}

// InvitationState es una invitacion recibida con lo que el calendario del buzon sabe de ella: el evento con su UID
// (EventID vacio si no esta), la direccion con la que el buzon esta invitado y su respuesta actual, y si el buzon es
// quien la organiza (una respuesta recibida).
type InvitationState struct {
	Invitation  domain.Invitation
	EventID     string
	Attendee    string
	PartStat    string
	IsOrganizer bool
}

func (uc *UseCase) eventByUID(ctx context.Context, p domain.Principal, uid string) (string, domain.Event, bool, error) {
	slug, err := uc.defaultCalendar(ctx, p)
	if err != nil {
		return "", domain.Event{}, false, err
	}
	e, err := uc.scheduling.EventByUID(ctx, p, slug, uid)
	if errors.Is(err, domain.ErrNotFound) {
		return slug, domain.Event{}, false, nil
	}
	return slug, e, err == nil, err
}

func myAttendee(inv domain.Invitation, addresses []string) string {
	mine := map[string]bool{}
	for _, a := range addresses {
		if n, err := domain.NormalizeAddress("address", a); err == nil {
			mine[n] = true
		}
	}
	for _, a := range inv.Attendees {
		if mine[a.Email] {
			return a.Email
		}
	}
	return ""
}

// InspectInvitation valida el iCalendar de un correo (acotado como un PUT) y lo cruza con el calendario del buzon.
// addresses son las direcciones del buzon.
func (uc *UseCase) InspectInvitation(ctx context.Context, p domain.Principal, raw string, addresses []string) (InvitationState, error) {
	if err := uc.schedulingStore(); err != nil {
		return InvitationState{}, err
	}
	inv, err := domain.ParseInvitation(raw, uc.cfg.Calendar)
	if err != nil {
		return InvitationState{}, err
	}
	state := InvitationState{Invitation: inv, Attendee: myAttendee(inv, addresses)}
	_, e, found, err := uc.eventByUID(ctx, p, inv.UID)
	if err != nil {
		return InvitationState{}, err
	}
	if found {
		state.EventID = eventID(e)
		state.IsOrganizer = domain.OrganizedBy(e.ICal, addresses)
		if state.Attendee != "" {
			state.PartStat = domain.PartStatOf(e.ICal, state.Attendee)
		}
	}
	return state, nil
}

func eventID(e domain.Event) string { return e.ResourceName[:len(e.ResourceName)-len(icsExt)] }

// InvitationAnswer es la respuesta a una invitacion: el REPLY para el organizador y el evento que quedo en el
// calendario del buzon (vacio si se rechazo).
type InvitationAnswer struct {
	Reply     string
	Organizer domain.Party
	Attendee  string
	EventID   string
}

// RespondInvitation acepta, deja en tentativo o rechaza una invitacion (REQUEST): aceptada o tentativa, se guarda
// en el calendario por defecto (sin METHOD, con la respuesta del buzon; si ya estaba con ese UID se reemplaza);
// rechazada, se quita si estaba. Una invitacion mas antigua que la guardada (SEQUENCE menor) no se aplica.
func (uc *UseCase) RespondInvitation(ctx context.Context, p domain.Principal, raw string, addresses []string, response string) (InvitationAnswer, error) {
	if err := uc.schedulingStore(); err != nil {
		return InvitationAnswer{}, err
	}
	inv, err := domain.ParseInvitation(raw, uc.cfg.Calendar)
	if err != nil {
		return InvitationAnswer{}, err
	}
	res, err := domain.RespondToInvitation(inv, addresses, response, uc.now())
	if err != nil {
		return InvitationAnswer{}, err
	}
	slug, existing, found, err := uc.eventByUID(ctx, p, inv.UID)
	if err != nil {
		return InvitationAnswer{}, err
	}
	if found && domain.SequenceOf(existing.ICal) > inv.Sequence {
		return InvitationAnswer{}, &domain.FieldError{Field: "sequence", Reason: "el calendario ya tiene una versión más reciente de esta invitación"}
	}
	out := InvitationAnswer{Reply: res.Reply, Organizer: *inv.Organizer, Attendee: res.Attendee}
	ps, _ := domain.NormalizePartStat("response", response)
	if ps == domain.PartStatDeclined {
		if found {
			if err := uc.calendars.DeleteEvent(ctx, p, slug, existing.ResourceName, domain.Precondition{IfMatch: []string{existing.ETag}}, uc.cfg.Limits.MaxChangesRetained); err != nil {
				return InvitationAnswer{}, err
			}
		}
		return out, nil
	}
	resource, cond := uuid.NewString()+icsExt, domain.Precondition{IfNoneMatchAny: true}
	if found {
		resource, cond = existing.ResourceName, domain.Precondition{IfMatch: []string{existing.ETag}}
	}
	view, err := uc.saveEvent(ctx, p, slug, resource, res.Stored, cond)
	if err != nil {
		return InvitationAnswer{}, err
	}
	out.EventID = view.ID
	return out, nil
}

// InvitationApplied es lo que hizo un REPLY o un CANCEL recibido en el calendario del buzon.
type InvitationApplied struct {
	Method  string
	Changed bool
	EventID string
}

// ApplyInvitation aplica al calendario del buzon un correo iTIP recibido. Un REPLY anota la respuesta del invitado
// en el evento que el buzon organiza, solo si la envia el propio invitado (from); un CANCEL retira el evento (o la
// aparicion que nombra) si lo envia su organizador. addresses son las direcciones del buzon.
func (uc *UseCase) ApplyInvitation(ctx context.Context, p domain.Principal, raw, from string, addresses []string) (InvitationApplied, error) {
	if err := uc.schedulingStore(); err != nil {
		return InvitationApplied{}, err
	}
	inv, err := domain.ParseInvitation(raw, uc.cfg.Calendar)
	if err != nil {
		return InvitationApplied{}, err
	}
	sender, err := domain.NormalizeAddress("from", from)
	if err != nil {
		return InvitationApplied{}, err
	}
	out := InvitationApplied{Method: inv.Method}
	slug, existing, found, err := uc.eventByUID(ctx, p, inv.UID)
	if err != nil {
		return InvitationApplied{}, err
	}
	if !found {
		return InvitationApplied{}, domain.ErrNotFound
	}
	out.EventID = eventID(existing)
	cond := domain.Precondition{IfMatch: []string{existing.ETag}}
	switch inv.Method {
	case domain.MethodReply:
		if !domain.OrganizedBy(existing.ICal, addresses) {
			return InvitationApplied{}, domain.ErrNotFound
		}
		updated, changed, err := domain.ApplyReply(existing.ICal, inv, sender)
		if err != nil || !changed {
			return out, err
		}
		if _, err := uc.saveEvent(ctx, p, slug, existing.ResourceName, updated, cond); err != nil {
			return InvitationApplied{}, err
		}
		out.Changed = true
	case domain.MethodCancel:
		org := domain.OrganizerOf(existing.ICal)
		if org == nil || inv.Organizer == nil || inv.Organizer.Email != org.Email || sender != org.Email {
			return InvitationApplied{}, &domain.FieldError{Field: "from", Reason: "la cancelación no la envía el organizador del evento"}
		}
		if inv.RecurrenceID == nil {
			if err := uc.calendars.DeleteEvent(ctx, p, slug, existing.ResourceName, cond, uc.cfg.Limits.MaxChangesRetained); err != nil {
				return InvitationApplied{}, err
			}
			out.EventID, out.Changed = "", true
			return out, nil
		}
		updated, err := domain.DeleteOccurrence(existing.ICal, *inv.RecurrenceID, uc.now())
		if errors.Is(err, domain.ErrNotFound) {
			return out, nil
		}
		if err != nil {
			return InvitationApplied{}, err
		}
		if _, err := uc.saveEvent(ctx, p, slug, existing.ResourceName, updated, cond); err != nil {
			return InvitationApplied{}, err
		}
		out.Changed = true
	default:
		return InvitationApplied{}, &domain.FieldError{Field: "method", Reason: "una invitación (REQUEST) se responde, no se aplica"}
	}
	return out, nil
}

// UpdateOccurrence cambia una sola aparicion de una serie del calendario por defecto (ver domain.SetOccurrence).
func (uc *UseCase) UpdateOccurrence(ctx context.Context, p domain.Principal, id string, rid time.Time, in domain.EventFields, cond domain.Precondition) (EventView, error) {
	f, err := in.Normalize()
	if err != nil {
		return EventView{}, err
	}
	return uc.rewriteEvent(ctx, p, id, cond, func(cur string) (string, error) {
		return domain.SetOccurrence(cur, rid, f, uc.now())
	})
}

// DeleteOccurrence borra una sola aparicion de una serie del calendario por defecto (ver domain.DeleteOccurrence).
func (uc *UseCase) DeleteOccurrence(ctx context.Context, p domain.Principal, id string, rid time.Time, cond domain.Precondition) (EventView, error) {
	return uc.rewriteEvent(ctx, p, id, cond, func(cur string) (string, error) {
		return domain.DeleteOccurrence(cur, rid, uc.now())
	})
}

func (uc *UseCase) rewriteEvent(ctx context.Context, p domain.Principal, id string, cond domain.Precondition, change func(cur string) (string, error)) (EventView, error) {
	resource, ok := eventResource(id)
	if !ok {
		return EventView{}, domain.ErrNotFound
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
		raw, err := change(cur.ICal)
		if err != nil {
			return err
		}
		out, err = uc.saveEvent(ctx, p, slug, resource, raw, domain.Precondition{IfMatch: []string{cur.ETag}})
		return err
	})
	return out, err
}
