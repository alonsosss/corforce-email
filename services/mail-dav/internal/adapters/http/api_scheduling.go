package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	apiresponse "github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/services/mail-dav/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-dav/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// Planificacion (docs/adr/0004, "Invitaciones, disponibilidad y citas"): una aparicion de una serie, las
// invitaciones iTIP, la disponibilidad del equipo y las paginas de citas.

// decodeLimit es decode con otro tope de cuerpo.
func (a *API) decodeLimit(w http.ResponseWriter, r *http.Request, v any, limit int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	err := json.NewDecoder(r.Body).Decode(v)
	var tooLarge *http.MaxBytesError
	switch {
	case err == nil:
		return true
	case errors.As(err, &tooLarge):
		limitExceeded(w, http.StatusRequestEntityTooLarge, "body", "el cuerpo supera el tamano maximo")
	default:
		apiresponse.ErrBadRequest(w, "el cuerpo no es un JSON valido")
	}
	return false
}

func recurrenceIDParam(r *http.Request) (time.Time, error) {
	return domain.ParseEventTime("recurrence_id", chi.URLParam(r, "rid"), false)
}

func (a *API) updateOccurrence(w http.ResponseWriter, r *http.Request) {
	rid, err := recurrenceIDParam(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	f, ok := a.readEvent(w, r)
	if !ok {
		return
	}
	f.Recurrence = nil
	v, err := a.uc.UpdateOccurrence(r.Context(), principalOf(r), chi.URLParam(r, "id"), rid, f, ifMatch(r))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	setETag(w, v.ETag)
	apiresponse.JSON(w, http.StatusOK, eventOut(v))
}

func (a *API) deleteOccurrence(w http.ResponseWriter, r *http.Request) {
	rid, err := recurrenceIDParam(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	v, err := a.uc.DeleteOccurrence(r.Context(), principalOf(r), chi.URLParam(r, "id"), rid, ifMatch(r))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	setETag(w, v.ETag)
	apiresponse.JSON(w, http.StatusOK, eventOut(v))
}

type outgoingJSON struct {
	Method     string   `json:"method"`
	ICal       string   `json:"ical"`
	Recipients []string `json:"recipients"`
}

func outgoingOut(o domain.Outgoing) outgoingJSON {
	out := outgoingJSON{Method: o.Method, ICal: o.ICal, Recipients: o.Recipients}
	if out.Recipients == nil {
		out.Recipients = []string{}
	}
	return out
}

type eventInvitationJSON struct {
	outgoingJSON
	Event eventJSON `json:"event"`
}

// eventInvitation escribe la invitacion (REQUEST o CANCEL) de un evento del buzon para que el webmail la envie.
func (a *API) eventInvitation(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Method string `json:"method"`
	}
	if !a.decode(w, r, &in) {
		return
	}
	out, v, err := a.uc.EventInvitation(r.Context(), principalOf(r), chi.URLParam(r, "id"), strings.ToUpper(strings.TrimSpace(in.Method)))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	apiresponse.JSON(w, http.StatusOK, eventInvitationJSON{outgoingJSON: outgoingOut(out), Event: eventOut(v)})
}

type itipInput struct {
	ICal      string   `json:"ical"`
	Addresses []string `json:"addresses"`
	Response  string   `json:"response"`
	From      string   `json:"from"`
}

const maxITIPAddresses = 50

func (a *API) readITIP(w http.ResponseWriter, r *http.Request) (itipInput, bool) {
	var in itipInput
	if !a.decodeLimit(w, r, &in, a.lim.MaxITIPBodyBytes) {
		return in, false
	}
	if strings.TrimSpace(in.ICal) == "" {
		a.fail(w, r, &domain.FieldError{Field: "ical", Reason: "es obligatorio"})
		return in, false
	}
	if len(in.Addresses) > maxITIPAddresses {
		a.fail(w, r, &domain.FieldError{Field: "addresses", Reason: "demasiadas direcciones"})
		return in, false
	}
	return in, true
}

type invitationJSON struct {
	Method       string         `json:"method"`
	UID          string         `json:"uid"`
	Sequence     int            `json:"sequence"`
	Title        string         `json:"title"`
	Location     string         `json:"location"`
	Description  string         `json:"description"`
	Start        *string        `json:"start"`
	End          *string        `json:"end"`
	AllDay       bool           `json:"all_day"`
	TimeZone     string         `json:"timezone"`
	Recurring    bool           `json:"recurring"`
	RecurrenceID *string        `json:"recurrence_id"`
	Organizer    *partyJSON     `json:"organizer"`
	Attendees    []attendeeJSON `json:"attendees"`
	EventID      string         `json:"event_id"`
	Attendee     string         `json:"attendee"`
	PartStat     string         `json:"partstat"`
	IsOrganizer  bool           `json:"is_organizer"`
}

func optionalTime(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := formatTime(t)
	return &s
}

func invitationOut(s app.InvitationState) invitationJSON {
	inv := s.Invitation
	out := invitationJSON{
		Method: inv.Method, UID: inv.UID, Sequence: inv.Sequence, Title: inv.Summary, Location: inv.Location, Description: inv.Description,
		Start: optionalTime(inv.Start), End: optionalTime(inv.End), AllDay: inv.AllDay, TimeZone: inv.TimeZone, Recurring: inv.Recurring,
		Attendees: []attendeeJSON{}, EventID: s.EventID, Attendee: s.Attendee, PartStat: s.PartStat, IsOrganizer: s.IsOrganizer,
	}
	if inv.RecurrenceID != nil {
		out.RecurrenceID = optionalTime(*inv.RecurrenceID)
	}
	if inv.Organizer != nil {
		out.Organizer = &partyJSON{Email: inv.Organizer.Email, Name: inv.Organizer.Name}
	}
	for _, at := range inv.Attendees {
		out.Attendees = append(out.Attendees, attendeeJSON{Email: at.Email, Name: at.Name, PartStat: at.PartStat})
	}
	return out
}

// inspectInvitation valida el iCalendar de un correo recibido y lo cruza con el calendario del buzon.
func (a *API) inspectInvitation(w http.ResponseWriter, r *http.Request) {
	in, ok := a.readITIP(w, r)
	if !ok {
		return
	}
	s, err := a.uc.InspectInvitation(r.Context(), principalOf(r), in.ICal, in.Addresses)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	apiresponse.JSON(w, http.StatusOK, invitationOut(s))
}

// respondInvitation acepta, deja en tentativo o rechaza una invitacion y devuelve el REPLY para el organizador.
func (a *API) respondInvitation(w http.ResponseWriter, r *http.Request) {
	in, ok := a.readITIP(w, r)
	if !ok {
		return
	}
	res, err := a.uc.RespondInvitation(r.Context(), principalOf(r), in.ICal, in.Addresses, in.Response)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	apiresponse.JSON(w, http.StatusOK, map[string]any{
		"reply": res.Reply, "organizer": partyJSON{Email: res.Organizer.Email, Name: res.Organizer.Name},
		"attendee": res.Attendee, "event_id": res.EventID,
	})
}

// applyInvitation aplica una respuesta (REPLY) o una cancelacion (CANCEL) recibidas.
func (a *API) applyInvitation(w http.ResponseWriter, r *http.Request) {
	in, ok := a.readITIP(w, r)
	if !ok {
		return
	}
	res, err := a.uc.ApplyInvitation(r.Context(), principalOf(r), in.ICal, in.From, in.Addresses)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	apiresponse.JSON(w, http.StatusOK, map[string]any{"method": res.Method, "changed": res.Changed, "event_id": res.EventID})
}

type intervalJSON struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

func intervalsOut(list []domain.Interval) []intervalJSON {
	out := make([]intervalJSON, len(list))
	for i, iv := range list {
		out[i] = intervalJSON{Start: formatTime(iv.Start), End: formatTime(iv.End)}
	}
	return out
}

// availability devuelve la ocupacion (solo inicio y fin) de buzones de la misma empresa.
func (a *API) availability(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Addresses []string `json:"addresses"`
		Start     string   `json:"start"`
		End       string   `json:"end"`
	}
	if !a.decode(w, r, &in) {
		return
	}
	start, err := domain.ParseEventTime("start", in.Start, false)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	end, err := domain.ParseEventTime("end", in.End, false)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	list, err := a.uc.Availability(r.Context(), principalOf(r), in.Addresses, start, end)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	type item struct {
		Address string         `json:"address"`
		Known   bool           `json:"known"`
		Partial bool           `json:"partial"`
		Busy    []intervalJSON `json:"busy"`
	}
	out := make([]item, len(list))
	for i, m := range list {
		out[i] = item{Address: m.Address, Known: m.Known, Partial: m.Partial, Busy: intervalsOut(m.Busy)}
	}
	apiresponse.JSON(w, http.StatusOK, out)
}

type windowJSON struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

type bookingSettingsJSON struct {
	Title            string                  `json:"title"`
	Description      string                  `json:"description"`
	DurationMinutes  int                     `json:"duration_minutes"`
	BufferMinutes    int                     `json:"buffer_minutes"`
	MinNoticeMinutes int                     `json:"min_notice_minutes"`
	MaxAdvanceDays   int                     `json:"max_advance_days"`
	DailyLimit       int                     `json:"daily_limit"`
	TimeZone         string                  `json:"timezone"`
	Weekly           map[string][]windowJSON `json:"weekly"`
	Active           bool                    `json:"active"`
}

type bookingPageJSON struct {
	bookingSettingsJSON
	PublicID     string    `json:"public_id"`
	OwnerAddress string    `json:"owner_address"`
	OwnerName    string    `json:"owner_name"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (in bookingSettingsJSON) settings() (domain.BookingSettings, error) {
	s := domain.BookingSettings{
		Title: in.Title, Description: in.Description, DurationMinutes: in.DurationMinutes, BufferMinutes: in.BufferMinutes,
		MinNoticeMinutes: in.MinNoticeMinutes, MaxAdvanceDays: in.MaxAdvanceDays, DailyLimit: in.DailyLimit, TimeZone: in.TimeZone, Active: in.Active,
	}
	for code, day := range in.Weekly {
		d, ok := domain.WeekdayOf(code)
		if !ok {
			return s, &domain.FieldError{Field: "weekly", Reason: "se espera un dia MO, TU, WE, TH, FR, SA o SU"}
		}
		if len(day) > domain.MaxBookingWindowsPerDay {
			return s, &domain.FieldError{Field: "weekly." + code, Reason: "demasiadas franjas en el dia"}
		}
		for _, win := range day {
			start, err := domain.ParseClock("weekly."+code, win.Start)
			if err != nil {
				return s, err
			}
			end, err := domain.ParseClock("weekly."+code, win.End)
			if err != nil {
				return s, err
			}
			s.Weekly[d] = append(s.Weekly[d], domain.DayWindow{Start: start, End: end})
		}
	}
	return s, nil
}

func weeklyOut(w [7][]domain.DayWindow) map[string][]windowJSON {
	out := map[string][]windowJSON{}
	for d := time.Sunday; d <= time.Saturday; d++ {
		list := []windowJSON{}
		for _, win := range w[d] {
			list = append(list, windowJSON{Start: domain.FormatClock(win.Start), End: domain.FormatClock(win.End)})
		}
		out[domain.WeekdayCode(d)] = list
	}
	return out
}

func bookingPageOut(p domain.BookingPage) bookingPageJSON {
	return bookingPageJSON{
		bookingSettingsJSON: bookingSettingsJSON{
			Title: p.Title, Description: p.Description, DurationMinutes: p.DurationMinutes, BufferMinutes: p.BufferMinutes,
			MinNoticeMinutes: p.MinNoticeMinutes, MaxAdvanceDays: p.MaxAdvanceDays, DailyLimit: p.DailyLimit, TimeZone: p.TimeZone,
			Weekly: weeklyOut(p.Weekly), Active: p.Active,
		},
		PublicID: p.PublicID, OwnerAddress: p.OwnerAddress, OwnerName: p.OwnerName, UpdatedAt: p.UpdatedAt.UTC(),
	}
}

func (a *API) bookingSettings(w http.ResponseWriter, r *http.Request) {
	p, err := a.uc.BookingSettings(r.Context(), principalOf(r))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	apiresponse.JSON(w, http.StatusOK, bookingPageOut(p))
}

func (a *API) saveBookingSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		bookingSettingsJSON
		OwnerName      string `json:"owner_name"`
		RegenerateLink bool   `json:"regenerate_link"`
	}
	if !a.decode(w, r, &in) {
		return
	}
	s, err := in.settings()
	if err != nil {
		a.fail(w, r, err)
		return
	}
	p, err := a.uc.SaveBookingSettings(r.Context(), principalOf(r), s, in.OwnerName, in.RegenerateLink)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	apiresponse.JSON(w, http.StatusOK, bookingPageOut(p))
}

// publicTarget lee la empresa y el enlace de una ruta publica; lo que no tiene su forma es un 404, como una pagina
// que no existe.
func (a *API) publicTarget(w http.ResponseWriter, r *http.Request) (uuid.UUID, string, bool) {
	tenant, err := uuid.Parse(chi.URLParam(r, "tenant"))
	page := chi.URLParam(r, "page")
	if err != nil || tenant == uuid.Nil || !domain.ValidPublicID(page) {
		apiresponse.ErrNotFound(w, "no existe")
		return uuid.Nil, "", false
	}
	return tenant, page, true
}

// publicBooking es la pagina activa y sus huecos libres en [start, end). La direccion del dueno es para el webmail
// (comprueba que el dueno es de su celda antes de reservar) y el webmail no la reenvia al visitante.
func (a *API) publicBooking(w http.ResponseWriter, r *http.Request) {
	tenant, page, ok := a.publicTarget(w, r)
	if !ok {
		return
	}
	start, err := domain.ParseEventTime("start", r.URL.Query().Get("start"), false)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	end, err := domain.ParseEventTime("end", r.URL.Query().Get("end"), false)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	res, err := a.uc.PublicBookingSlots(r.Context(), tenant, page, start, end)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	p := res.Page
	apiresponse.JSON(w, http.StatusOK, map[string]any{
		"title": p.Title, "description": p.Description, "duration_minutes": p.DurationMinutes, "timezone": p.TimeZone,
		"owner_name": p.OwnerName, "owner_address": p.OwnerAddress, "min_notice_minutes": p.MinNoticeMinutes, "max_advance_days": p.MaxAdvanceDays,
		"slots": intervalsOut(res.Slots),
	})
}

// book reserva un hueco. Devuelve lo que el webmail necesita para invitar desde el buzon del dueno: su direccion,
// la cita y la invitacion (sin la nota del visitante).
func (a *API) book(w http.ResponseWriter, r *http.Request) {
	tenant, page, ok := a.publicTarget(w, r)
	if !ok {
		return
	}
	var in struct {
		Start string `json:"start"`
		Name  string `json:"name"`
		Email string `json:"email"`
		Note  string `json:"note"`
	}
	if !a.decode(w, r, &in) {
		return
	}
	start, err := domain.ParseEventTime("start", in.Start, false)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	res, err := a.uc.Book(r.Context(), tenant, page, domain.BookingRequest{Start: start, Name: in.Name, Email: in.Email, Note: in.Note})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	f := res.Event.Fields
	apiresponse.JSON(w, http.StatusCreated, map[string]any{
		"event_id": res.Event.ID, "title": f.Title, "start": formatTime(f.Start), "end": formatTime(f.End), "timezone": f.TimeZone,
		"owner":      partyJSON{Email: res.Page.OwnerAddress, Name: res.Page.OwnerName},
		"invitation": outgoingOut(res.Invitation),
	})
}
