package http

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"github.com/go-chi/chi/v5"
)

// PublicBookingPath es el prefijo de la pagina publica de citas: ruta publica del gateway (sin sesion), enrutada
// por la celda que va en el primer segmento.
const PublicBookingPath = "/api/v1/public/booking"

const (
	maxBookingBody    = 16 << 10
	maxBookingSettBdy = 32 << 10
	maxResponseBody   = 1 << 10
)

// notifyParam dice si un cambio del calendario envia invitaciones: si, salvo notify=false.
func notifyParam(r *http.Request) bool { return r.URL.Query().Get("notify") != "false" }

type deliveryDTO struct {
	Method     string `json:"method"`
	Recipients int    `json:"recipients"`
	Sent       bool   `json:"sent"`
}

func toDeliveryDTO(d *domain.InvitationDelivery) *deliveryDTO {
	if d == nil {
		return nil
	}
	return &deliveryDTO{Method: d.Method, Recipients: d.Recipients, Sent: d.Sent}
}

type savedEventDTO struct {
	eventDTO
	Invitations *deliveryDTO `json:"invitations"`
}

func toSavedEventDTO(e domain.SavedEvent) savedEventDTO {
	return savedEventDTO{eventDTO: toEventDTO(e.Event), Invitations: toDeliveryDTO(e.Delivery)}
}

// UpdateOccurrence cambia una sola aparicion de una serie ({rid} es su RECURRENCE-ID en RFC 3339).
func (h *Handler) UpdateOccurrence(w http.ResponseWriter, r *http.Request) {
	var req eventInputDTO
	if err := validate.DecodeJSONLimit(w, r, &req, maxEventBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	ctx, cancel := h.opContext(r)
	defer cancel()
	e, err := h.app.UpdateOccurrence(ctx, sessionFrom(r), chi.URLParam(r, "id"), chi.URLParam(r, "rid"), req.toDomain(), ifMatch(r), notifyParam(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	withETag(w, e.ETag)
	response.JSON(w, http.StatusOK, toSavedEventDTO(e))
}

// DeleteOccurrence borra una sola aparicion de una serie.
func (h *Handler) DeleteOccurrence(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.opContext(r)
	defer cancel()
	e, err := h.app.DeleteOccurrence(ctx, sessionFrom(r), chi.URLParam(r, "id"), chi.URLParam(r, "rid"), ifMatch(r), notifyParam(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	withETag(w, e.ETag)
	response.JSON(w, http.StatusOK, toSavedEventDTO(e))
}

type invitationDTO struct {
	Method       string        `json:"method"`
	UID          string        `json:"uid"`
	Sequence     int           `json:"sequence"`
	Title        string        `json:"title"`
	Location     string        `json:"location"`
	Description  string        `json:"description"`
	Start        *string       `json:"start"`
	End          *string       `json:"end"`
	AllDay       bool          `json:"all_day"`
	TimeZone     string        `json:"timezone"`
	Recurring    bool          `json:"recurring"`
	RecurrenceID *string       `json:"recurrence_id"`
	Organizer    *partyDTO     `json:"organizer"`
	Attendees    []attendeeDTO `json:"attendees"`
	EventID      string        `json:"event_id"`
	Attendee     string        `json:"attendee"`
	PartStat     string        `json:"partstat"`
	IsOrganizer  bool          `json:"is_organizer"`
}

func toInvitationDTO(inv domain.Invitation) invitationDTO {
	out := invitationDTO{
		Method: inv.Method, UID: inv.UID, Sequence: inv.Sequence, Title: inv.Title, Location: inv.Location, Description: inv.Description,
		Start: inv.Start, End: inv.End, AllDay: inv.AllDay, TimeZone: inv.TimeZone, Recurring: inv.Recurring, RecurrenceID: inv.RecurrenceID,
		Attendees: []attendeeDTO{}, EventID: inv.EventID, Attendee: inv.Attendee, PartStat: inv.PartStat, IsOrganizer: inv.IsOrganizer,
	}
	if o := inv.Organizer; o != nil {
		out.Organizer = &partyDTO{Email: o.Email, Name: o.Name}
	}
	for _, a := range inv.Attendees {
		out.Attendees = append(out.Attendees, attendeeDTO{Email: a.Email, Name: a.Name, PartStat: a.PartStat})
	}
	return out
}

// Invitation muestra la invitacion (text/calendar) de un mensaje cruzada con el calendario del buzon.
func (h *Handler) Invitation(w http.ResponseWriter, r *http.Request) {
	folder, uid, err := messageParams(r)
	if err != nil {
		writeError(w, err)
		return
	}
	ctx, cancel := h.opContext(r)
	defer cancel()
	inv, err := h.app.Invitation(ctx, sessionFrom(r), folder, uid)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	response.JSON(w, http.StatusOK, toInvitationDTO(inv))
}

// RespondInvitation acepta, deja en tentativo o rechaza la invitacion de un mensaje.
func (h *Handler) RespondInvitation(w http.ResponseWriter, r *http.Request) {
	folder, uid, err := messageParams(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var req struct {
		Response string `json:"response"`
	}
	if err := validate.DecodeJSONLimit(w, r, &req, maxResponseBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	ctx, cancel := h.opContext(r)
	defer cancel()
	res, err := h.app.RespondInvitation(ctx, sessionFrom(r), folder, uid, req.Response)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]any{"event_id": res.EventID, "reply_sent": res.ReplySent})
}

// ApplyInvitation aplica al calendario la respuesta o la cancelacion de un mensaje.
func (h *Handler) ApplyInvitation(w http.ResponseWriter, r *http.Request) {
	folder, uid, err := messageParams(r)
	if err != nil {
		writeError(w, err)
		return
	}
	ctx, cancel := h.opContext(r)
	defer cancel()
	res, err := h.app.ApplyInvitation(ctx, sessionFrom(r), folder, uid)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]any{"method": res.Method, "changed": res.Changed, "event_id": res.EventID})
}

type busyDTO struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

func toBusyDTOs(list []domain.BusyInterval) []busyDTO {
	out := make([]busyDTO, len(list))
	for i, b := range list {
		out[i] = busyDTO{Start: b.Start, End: b.End}
	}
	return out
}

// Availability devuelve la ocupacion (solo inicio y fin) de companeros: ?addresses=a,b&start=&end= (RFC 3339).
func (h *Handler) Availability(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	window, err := domain.NewEventWindow(q.Get("start"), q.Get("end"))
	if err != nil {
		writeError(w, err)
		return
	}
	ctx, cancel := h.opContext(r)
	defer cancel()
	list, err := h.app.Availability(ctx, sessionFrom(r), q["addresses"], window)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	type item struct {
		Address string    `json:"address"`
		Known   bool      `json:"known"`
		Partial bool      `json:"partial"`
		Busy    []busyDTO `json:"busy"`
	}
	out := make([]item, len(list))
	for i, a := range list {
		out[i] = item{Address: a.Address, Known: a.Known, Partial: a.Partial, Busy: toBusyDTOs(a.Busy)}
	}
	response.JSON(w, http.StatusOK, out)
}

type bookingWindowDTO struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

type bookingSettingsDTO struct {
	Title            string                        `json:"title"`
	Description      string                        `json:"description"`
	DurationMinutes  int                           `json:"duration_minutes"`
	BufferMinutes    int                           `json:"buffer_minutes"`
	MinNoticeMinutes int                           `json:"min_notice_minutes"`
	MaxAdvanceDays   int                           `json:"max_advance_days"`
	DailyLimit       int                           `json:"daily_limit"`
	TimeZone         string                        `json:"timezone"`
	Weekly           map[string][]bookingWindowDTO `json:"weekly"`
	Active           bool                          `json:"active"`
}

type bookingPageDTO struct {
	bookingSettingsDTO
	PublicID     string     `json:"public_id"`
	OwnerAddress string     `json:"owner_address"`
	OwnerName    string     `json:"owner_name"`
	UpdatedAt    *time.Time `json:"updated_at"`
	Cell         string     `json:"cell"`
	TenantID     string     `json:"tenant_id"`
}

func (in bookingSettingsDTO) toDomain() domain.BookingSettings {
	out := domain.BookingSettings{
		Title: in.Title, Description: in.Description, DurationMinutes: in.DurationMinutes, BufferMinutes: in.BufferMinutes,
		MinNoticeMinutes: in.MinNoticeMinutes, MaxAdvanceDays: in.MaxAdvanceDays, DailyLimit: in.DailyLimit, TimeZone: in.TimeZone,
		Weekly: map[string][]domain.BookingWindow{}, Active: in.Active,
	}
	for day, list := range in.Weekly {
		for _, win := range list {
			out.Weekly[day] = append(out.Weekly[day], domain.BookingWindow{Start: win.Start, End: win.End})
		}
	}
	return out
}

func toBookingPageDTO(p domain.BookingPage) bookingPageDTO {
	out := bookingPageDTO{
		bookingSettingsDTO: bookingSettingsDTO{
			Title: p.Title, Description: p.Description, DurationMinutes: p.DurationMinutes, BufferMinutes: p.BufferMinutes,
			MinNoticeMinutes: p.MinNoticeMinutes, MaxAdvanceDays: p.MaxAdvanceDays, DailyLimit: p.DailyLimit, TimeZone: p.TimeZone,
			Weekly: map[string][]bookingWindowDTO{}, Active: p.Active,
		},
		PublicID: p.PublicID, OwnerAddress: p.OwnerAddress, OwnerName: p.OwnerName, UpdatedAt: p.UpdatedAt, Cell: p.Cell, TenantID: p.TenantID,
	}
	for day, list := range p.Weekly {
		windows := make([]bookingWindowDTO, len(list))
		for i, win := range list {
			windows[i] = bookingWindowDTO{Start: win.Start, End: win.End}
		}
		out.Weekly[day] = windows
	}
	return out
}

// BookingSettings devuelve la pagina de citas del buzon (404 si aun no la configuro).
func (h *Handler) BookingSettings(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.opContext(r)
	defer cancel()
	p, err := h.app.BookingSettings(ctx, sessionFrom(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	response.JSON(w, http.StatusOK, toBookingPageDTO(p))
}

// SaveBookingSettings guarda la pagina de citas; regenerate_link cambia su enlace.
func (h *Handler) SaveBookingSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		bookingSettingsDTO
		RegenerateLink bool `json:"regenerate_link"`
	}
	if err := validate.DecodeJSONLimit(w, r, &req, maxBookingSettBdy); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	ctx, cancel := h.opContext(r)
	defer cancel()
	p, err := h.app.SaveBookingSettings(ctx, sessionFrom(r), req.toDomain(), req.RegenerateLink)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	response.JSON(w, http.StatusOK, toBookingPageDTO(p))
}

// PublicBooking es la pagina publica de citas: su configuracion visible y los huecos libres en [start, end).
func (h *Handler) PublicBooking(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	window, err := domain.NewEventWindow(q.Get("start"), q.Get("end"))
	if err != nil {
		writeError(w, err)
		return
	}
	ctx, cancel := h.opContext(r)
	defer cancel()
	p, err := h.app.PublicBooking(ctx, chi.URLParam(r, "cell"), chi.URLParam(r, "tenant"), chi.URLParam(r, "page"), window)
	if err != nil {
		writePublicError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]any{
		"title": p.Title, "description": p.Description, "duration_minutes": p.DurationMinutes, "timezone": p.TimeZone,
		"owner_name": p.OwnerName, "min_notice_minutes": p.MinNoticeMinutes, "max_advance_days": p.MaxAdvanceDays,
		"slots": toBusyDTOs(p.Slots),
	})
}

// Book reserva una cita desde la pagina publica.
func (h *Handler) Book(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Start   string `json:"start"`
		Name    string `json:"name"`
		Email   string `json:"email"`
		Note    string `json:"note"`
		Website string `json:"website"`
	}
	if err := validate.DecodeJSONLimit(w, r, &req, maxBookingBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	// El plazo de una reserva incluye el envio de la confirmacion, como el de un correo.
	ctx, cancel := context.WithTimeout(r.Context(), h.cfg.TransferTimeout)
	defer cancel()
	res, err := h.app.Book(ctx, chi.URLParam(r, "cell"), chi.URLParam(r, "tenant"), chi.URLParam(r, "page"),
		domain.BookingRequest{Start: req.Start, Name: req.Name, Email: req.Email, Note: req.Note, Website: req.Website})
	if err != nil {
		writePublicError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, map[string]any{
		"title": res.Title, "start": res.Start, "end": res.End, "timezone": res.TimeZone, "owner_name": res.OwnerName,
		"confirmation_sent": res.ConfirmationSent,
	})
}

// writePublicError responde a la pagina publica sin nada de la sesion: un enlace que no existe es 404 y lo demas
// pasa por la traduccion comun.
func writePublicError(w http.ResponseWriter, err error) {
	if errors.Is(err, domain.ErrResourceNotFound) {
		response.ErrNotFound(w, "la pagina de citas no existe")
		return
	}
	writeError(w, err)
}
