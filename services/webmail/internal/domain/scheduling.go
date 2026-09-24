package domain

import (
	"regexp"
	"strings"
	"time"
)

// Planificacion (docs/Plan_Webmail_Innovador.md, bloque C3): invitaciones iTIP, disponibilidad del equipo y
// pagina publica de citas. Los datos y su validacion son de mail-dav; el webmail envia y recibe el correo.

// Party es el organizador de un evento.
type Party struct {
	Email string
	Name  string
}

// Attendee es un invitado con su respuesta (PARTSTAT).
type Attendee struct {
	Email    string
	Name     string
	PartStat string
}

// Metodos iTIP que el webmail envia o procesa.
const (
	MethodRequest = "REQUEST"
	MethodReply   = "REPLY"
	MethodCancel  = "CANCEL"
)

// Respuestas a una invitacion.
const (
	PartStatAccepted  = "ACCEPTED"
	PartStatTentative = "TENTATIVE"
	PartStatDeclined  = "DECLINED"
)

// ITIPMessage es un iCalendar con METHOD listo para enviar y a quien va.
type ITIPMessage struct {
	Method     string
	ICal       string
	Recipients []string
}

// InvitationDelivery dice si salio la invitacion de un cambio del calendario: a cuantos destinatarios y con que
// metodo. Sent falso con destinatarios es un envio que fallo (el cambio del calendario se hizo igual).
type InvitationDelivery struct {
	Method     string
	Recipients int
	Sent       bool
}

// SavedEvent es un evento guardado con lo que paso con su invitacion (nil si no habia que enviar nada).
type SavedEvent struct {
	Event
	Delivery *InvitationDelivery
}

// Invitation es una invitacion recibida (text/calendar de un mensaje), validada por mail-dav y cruzada con el
// calendario del buzon.
type Invitation struct {
	Method       string
	UID          string
	Sequence     int
	Title        string
	Location     string
	Description  string
	Start        *string
	End          *string
	AllDay       bool
	TimeZone     string
	Recurring    bool
	RecurrenceID *string
	Organizer    *Party
	Attendees    []Attendee
	EventID      string
	Attendee     string
	PartStat     string
	IsOrganizer  bool
}

// InvitationAnswer es la respuesta de mail-dav a aceptar, dejar en tentativo o rechazar una invitacion.
type InvitationAnswer struct {
	Reply     string
	Organizer Party
	Attendee  string
	EventID   string
}

// InvitationResult es lo que ve el usuario tras responder: el evento que quedo en su calendario y si la respuesta
// llego al organizador.
type InvitationResult struct {
	EventID   string
	ReplySent bool
}

// InvitationApplied es lo que hizo una respuesta o una cancelacion recibida en el calendario del buzon.
type InvitationApplied struct {
	Method  string
	Changed bool
	EventID string
}

// ValidateInvitationResponse admite ACCEPTED, TENTATIVE o DECLINED sin distinguir mayusculas.
func ValidateInvitationResponse(v string) (string, error) {
	switch r := strings.ToUpper(strings.TrimSpace(v)); r {
	case PartStatAccepted, PartStatTentative, PartStatDeclined:
		return r, nil
	}
	return "", invalid("response", "se espera ACCEPTED, TENTATIVE o DECLINED")
}

// IsCalendarPart dice si una parte de un mensaje lleva una invitacion (RFC 6047).
func IsCalendarPart(contentType string) bool {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	return ct == "text/calendar" || ct == "application/ics"
}

// BusyInterval es un tramo ocupado o libre [Start, End) en RFC 3339.
type BusyInterval struct {
	Start string
	End   string
}

// MailboxAvailability es la ocupacion de un buzon de la empresa: solo inicio y fin.
type MailboxAvailability struct {
	Address string
	Known   bool
	Partial bool
	Busy    []BusyInterval
}

// MaxAvailabilityAddresses acota la peticion antes de llegar a mail-dav, que aplica su propio tope.
const MaxAvailabilityAddresses = 50

// NewAvailabilityQuery valida las direcciones de una consulta de disponibilidad.
func NewAvailabilityQuery(raw []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, r := range raw {
		for _, part := range strings.Split(r, ",") {
			if part = strings.TrimSpace(part); part == "" {
				continue
			}
			a, err := NewAddress("addresses", "", part)
			if err != nil {
				return nil, err
			}
			key := strings.ToLower(a.Email)
			if !seen[key] {
				seen[key] = true
				out = append(out, key)
			}
		}
	}
	if len(out) == 0 {
		return nil, invalid("addresses", "hace falta al menos una dirección")
	}
	if len(out) > MaxAvailabilityAddresses {
		return nil, invalid("addresses", "demasiadas direcciones")
	}
	return out, nil
}

// BookingWindow es una franja de un dia de la semana (HH:MM).
type BookingWindow struct {
	Start string
	End   string
}

// BookingSettings es la configuracion de la pagina de citas; la valida mail-dav.
type BookingSettings struct {
	Title            string
	Description      string
	DurationMinutes  int
	BufferMinutes    int
	MinNoticeMinutes int
	MaxAdvanceDays   int
	DailyLimit       int
	TimeZone         string
	Weekly           map[string][]BookingWindow
	Active           bool
}

// BookingPage es la pagina de citas del buzon con su enlace: la celda, la empresa y el identificador publico, que
// la interfaz compone en la ruta de la pagina publica.
type BookingPage struct {
	BookingSettings
	PublicID     string
	OwnerAddress string
	OwnerName    string
	UpdatedAt    *time.Time
	Cell         string
	TenantID     string
}

// PublicBookingPage es lo que mail-dav devuelve de una pagina publica. OwnerAddress es para el webmail (comprobar
// que el dueno es de esta celda) y nunca sale al visitante.
type PublicBookingPage struct {
	Title            string
	Description      string
	DurationMinutes  int
	TimeZone         string
	OwnerName        string
	OwnerAddress     string
	MinNoticeMinutes int
	MaxAdvanceDays   int
	Slots            []BusyInterval
}

// BookingRequest es la reserva de un visitante. Website es la trampa para robots: un campo que la pagina oculta y
// que una persona deja vacio.
type BookingRequest struct {
	Start   string
	Name    string
	Email   string
	Note    string
	Website string
}

// BookingConfirmation es la cita que reservo mail-dav, con el dueno y la invitacion que hay que enviar.
type BookingConfirmation struct {
	EventID    string
	Title      string
	Start      string
	End        string
	TimeZone   string
	Owner      Party
	Invitation ITIPMessage
}

// BookingResult es lo que ve el visitante tras reservar.
type BookingResult struct {
	Title            string
	Start            string
	End              string
	TimeZone         string
	OwnerName        string
	ConfirmationSent bool
}

var publicIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{22,64}$`)

// ValidPublicBookingTarget dice si la celda, la empresa y el enlace de una ruta publica tienen su forma.
func ValidPublicBookingTarget(cell, tenant, page string) bool {
	return ValidCellCode(cell) && ValidUUID(tenant) && publicIDPattern.MatchString(page)
}
