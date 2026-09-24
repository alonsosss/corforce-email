package maildavcli

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/adapters/internalapi"
	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

// Planificacion de mail-dav (ports.Scheduling): /internal/mail-dav/calendar/events/{id}/occurrences y /itip,
// /internal/mail-dav/itip/*, /availability, /booking y /booking/public (esta sin buzon: la pagina publica).
const (
	itipPath          = "/internal/mail-dav/itip"
	availabilityPath  = "/internal/mail-dav/availability"
	bookingPath       = "/internal/mail-dav/booking"
	publicBookingPath = "/internal/mail-dav/booking/public/"
)

func occurrencePath(id, recurrenceID string) string {
	return eventsPath + "/" + url.PathEscape(id) + "/occurrences/" + url.PathEscape(recurrenceID)
}

func (c *Client) UpdateOccurrence(ctx context.Context, mb domain.MailboxRef, id, recurrenceID string, in domain.EventInput, ifMatch string) (domain.Event, error) {
	var out eventJSON
	err := c.api.Do(ctx, internalapi.Request{Method: http.MethodPut, Path: occurrencePath(id, recurrenceID),
		Header: withIfMatch(identity(mb), ifMatch), Body: toEventInput(in), Out: &out, Errors: passthrough})
	return out.toDomain(), err
}

func (c *Client) DeleteOccurrence(ctx context.Context, mb domain.MailboxRef, id, recurrenceID, ifMatch string) (domain.Event, error) {
	var out eventJSON
	err := c.api.Do(ctx, internalapi.Request{Method: http.MethodDelete, Path: occurrencePath(id, recurrenceID),
		Header: withIfMatch(identity(mb), ifMatch), Out: &out, Errors: passthrough})
	return out.toDomain(), err
}

type itipMessageJSON struct {
	Method     string   `json:"method"`
	ICal       string   `json:"ical"`
	Recipients []string `json:"recipients"`
}

func (m itipMessageJSON) toDomain() domain.ITIPMessage {
	return domain.ITIPMessage{Method: m.Method, ICal: m.ICal, Recipients: m.Recipients}
}

// EventInvitation no se reintenta: es un POST, aunque no escriba nada.
func (c *Client) EventInvitation(ctx context.Context, mb domain.MailboxRef, id, method string) (domain.ITIPMessage, error) {
	var out itipMessageJSON
	err := c.api.Do(ctx, internalapi.Request{Method: http.MethodPost, Path: eventsPath + "/" + url.PathEscape(id) + "/itip",
		Header: identity(mb), Body: map[string]string{"method": method}, Out: &out, Errors: passthrough})
	return out.toDomain(), err
}

type itipRequestJSON struct {
	ICal      string   `json:"ical"`
	Addresses []string `json:"addresses"`
	Response  string   `json:"response,omitempty"`
	From      string   `json:"from,omitempty"`
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

func (c *Client) InspectInvitation(ctx context.Context, mb domain.MailboxRef, ical string, addresses []string) (domain.Invitation, error) {
	var out invitationJSON
	err := c.api.Do(ctx, internalapi.Request{Method: http.MethodPost, Path: itipPath + "/inspect", Header: identity(mb),
		Body: itipRequestJSON{ICal: ical, Addresses: addresses}, Out: &out, Errors: passthrough})
	if err != nil {
		return domain.Invitation{}, err
	}
	inv := domain.Invitation{
		Method: out.Method, UID: out.UID, Sequence: out.Sequence, Title: out.Title, Location: out.Location, Description: out.Description,
		Start: out.Start, End: out.End, AllDay: out.AllDay, TimeZone: out.TimeZone, Recurring: out.Recurring, RecurrenceID: out.RecurrenceID,
		EventID: out.EventID, Attendee: out.Attendee, PartStat: out.PartStat, IsOrganizer: out.IsOrganizer, Attendees: []domain.Attendee{},
	}
	if out.Organizer != nil {
		inv.Organizer = &domain.Party{Email: out.Organizer.Email, Name: out.Organizer.Name}
	}
	for _, a := range out.Attendees {
		inv.Attendees = append(inv.Attendees, domain.Attendee{Email: a.Email, Name: a.Name, PartStat: a.PartStat})
	}
	return inv, nil
}

func (c *Client) RespondInvitation(ctx context.Context, mb domain.MailboxRef, ical string, addresses []string, response string) (domain.InvitationAnswer, error) {
	var out struct {
		Reply     string    `json:"reply"`
		Organizer partyJSON `json:"organizer"`
		Attendee  string    `json:"attendee"`
		EventID   string    `json:"event_id"`
	}
	err := c.api.Do(ctx, internalapi.Request{Method: http.MethodPost, Path: itipPath + "/respond", Header: identity(mb),
		Body: itipRequestJSON{ICal: ical, Addresses: addresses, Response: response}, Out: &out, Errors: passthrough})
	return domain.InvitationAnswer{Reply: out.Reply, Organizer: domain.Party{Email: out.Organizer.Email, Name: out.Organizer.Name},
		Attendee: out.Attendee, EventID: out.EventID}, err
}

func (c *Client) ApplyInvitation(ctx context.Context, mb domain.MailboxRef, ical, from string, addresses []string) (domain.InvitationApplied, error) {
	var out struct {
		Method  string `json:"method"`
		Changed bool   `json:"changed"`
		EventID string `json:"event_id"`
	}
	err := c.api.Do(ctx, internalapi.Request{Method: http.MethodPost, Path: itipPath + "/apply", Header: identity(mb),
		Body: itipRequestJSON{ICal: ical, Addresses: addresses, From: from}, Out: &out, Errors: passthrough})
	return domain.InvitationApplied{Method: out.Method, Changed: out.Changed, EventID: out.EventID}, err
}

type intervalJSON struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

func intervals(list []intervalJSON) []domain.BusyInterval {
	out := make([]domain.BusyInterval, len(list))
	for i, iv := range list {
		out[i] = domain.BusyInterval{Start: iv.Start, End: iv.End}
	}
	return out
}

// Availability es una consulta (POST con cuerpo), no una escritura.
func (c *Client) Availability(ctx context.Context, mb domain.MailboxRef, addresses []string, w domain.EventWindow) ([]domain.MailboxAvailability, error) {
	var rows []struct {
		Address string         `json:"address"`
		Known   bool           `json:"known"`
		Partial bool           `json:"partial"`
		Busy    []intervalJSON `json:"busy"`
	}
	err := c.api.Do(ctx, internalapi.Request{Method: http.MethodPost, Path: availabilityPath, Header: identity(mb),
		Body: map[string]any{"addresses": addresses, "start": w.Start.UTC().Format(time.RFC3339), "end": w.End.UTC().Format(time.RFC3339)},
		Out:  &rows, Errors: passthrough})
	if err != nil {
		return nil, err
	}
	out := make([]domain.MailboxAvailability, len(rows))
	for i, r := range rows {
		out[i] = domain.MailboxAvailability{Address: r.Address, Known: r.Known, Partial: r.Partial, Busy: intervals(r.Busy)}
	}
	return out, nil
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
	PublicID     string     `json:"public_id"`
	OwnerAddress string     `json:"owner_address"`
	OwnerName    string     `json:"owner_name"`
	UpdatedAt    *time.Time `json:"updated_at"`
}

func toBookingSettings(in domain.BookingSettings) bookingSettingsJSON {
	out := bookingSettingsJSON{
		Title: in.Title, Description: in.Description, DurationMinutes: in.DurationMinutes, BufferMinutes: in.BufferMinutes,
		MinNoticeMinutes: in.MinNoticeMinutes, MaxAdvanceDays: in.MaxAdvanceDays, DailyLimit: in.DailyLimit, TimeZone: in.TimeZone,
		Weekly: map[string][]windowJSON{}, Active: in.Active,
	}
	for day, list := range in.Weekly {
		windows := make([]windowJSON, len(list))
		for i, w := range list {
			windows[i] = windowJSON{Start: w.Start, End: w.End}
		}
		out.Weekly[day] = windows
	}
	return out
}

func (p bookingPageJSON) toDomain() domain.BookingPage {
	out := domain.BookingPage{
		BookingSettings: domain.BookingSettings{
			Title: p.Title, Description: p.Description, DurationMinutes: p.DurationMinutes, BufferMinutes: p.BufferMinutes,
			MinNoticeMinutes: p.MinNoticeMinutes, MaxAdvanceDays: p.MaxAdvanceDays, DailyLimit: p.DailyLimit, TimeZone: p.TimeZone,
			Weekly: map[string][]domain.BookingWindow{}, Active: p.Active,
		},
		PublicID: p.PublicID, OwnerAddress: p.OwnerAddress, OwnerName: p.OwnerName, UpdatedAt: p.UpdatedAt,
	}
	for day, list := range p.Weekly {
		windows := make([]domain.BookingWindow, len(list))
		for i, w := range list {
			windows[i] = domain.BookingWindow{Start: w.Start, End: w.End}
		}
		out.Weekly[day] = windows
	}
	return out
}

func (c *Client) BookingSettings(ctx context.Context, mb domain.MailboxRef) (domain.BookingPage, error) {
	var out bookingPageJSON
	err := c.api.Do(ctx, internalapi.Request{Method: http.MethodGet, Path: bookingPath, Header: identity(mb), Out: &out, Errors: passthrough})
	return out.toDomain(), err
}

func (c *Client) SaveBookingSettings(ctx context.Context, mb domain.MailboxRef, in domain.BookingSettings, ownerName string, regenerate bool) (domain.BookingPage, error) {
	body := struct {
		bookingSettingsJSON
		OwnerName      string `json:"owner_name"`
		RegenerateLink bool   `json:"regenerate_link"`
	}{toBookingSettings(in), ownerName, regenerate}
	var out bookingPageJSON
	err := c.api.Do(ctx, internalapi.Request{Method: http.MethodPut, Path: bookingPath, Header: identity(mb), Body: body, Out: &out, Errors: passthrough})
	return out.toDomain(), err
}

func publicPath(tenantID, publicID string) string {
	return publicBookingPath + url.PathEscape(tenantID) + "/" + url.PathEscape(publicID)
}

func (c *Client) PublicBooking(ctx context.Context, tenantID, publicID string, w domain.EventWindow) (domain.PublicBookingPage, error) {
	var out struct {
		Title            string         `json:"title"`
		Description      string         `json:"description"`
		DurationMinutes  int            `json:"duration_minutes"`
		TimeZone         string         `json:"timezone"`
		OwnerName        string         `json:"owner_name"`
		OwnerAddress     string         `json:"owner_address"`
		MinNoticeMinutes int            `json:"min_notice_minutes"`
		MaxAdvanceDays   int            `json:"max_advance_days"`
		Slots            []intervalJSON `json:"slots"`
	}
	query := url.Values{"start": {w.Start.UTC().Format(time.RFC3339)}, "end": {w.End.UTC().Format(time.RFC3339)}}
	err := c.api.Do(ctx, internalapi.Request{Method: http.MethodGet, Path: publicPath(tenantID, publicID), Query: query, Out: &out, Errors: passthrough})
	return domain.PublicBookingPage{
		Title: out.Title, Description: out.Description, DurationMinutes: out.DurationMinutes, TimeZone: out.TimeZone,
		OwnerName: out.OwnerName, OwnerAddress: out.OwnerAddress, MinNoticeMinutes: out.MinNoticeMinutes, MaxAdvanceDays: out.MaxAdvanceDays,
		Slots: intervals(out.Slots),
	}, err
}

// Book no se reintenta: crearia la cita dos veces.
func (c *Client) Book(ctx context.Context, tenantID, publicID string, in domain.BookingRequest) (domain.BookingConfirmation, error) {
	var out struct {
		EventID    string          `json:"event_id"`
		Title      string          `json:"title"`
		Start      string          `json:"start"`
		End        string          `json:"end"`
		TimeZone   string          `json:"timezone"`
		Owner      partyJSON       `json:"owner"`
		Invitation itipMessageJSON `json:"invitation"`
	}
	body := map[string]string{"start": in.Start, "name": in.Name, "email": in.Email, "note": in.Note}
	err := c.api.Do(ctx, internalapi.Request{Method: http.MethodPost, Path: publicPath(tenantID, publicID) + "/reservations", Body: body, Out: &out, Errors: passthrough})
	return domain.BookingConfirmation{
		EventID: out.EventID, Title: out.Title, Start: out.Start, End: out.End, TimeZone: out.TimeZone,
		Owner: domain.Party{Email: out.Owner.Email, Name: out.Owner.Name}, Invitation: out.Invitation.toDomain(),
	}, err
}
