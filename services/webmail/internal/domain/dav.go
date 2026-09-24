package domain

import (
	"regexp"
	"time"
)

// MailboxRef identifica el buzon ante mail-dav, que vive en la base de la empresa: la empresa y
// el buzon que mail-auth devolvio al abrir la sesion. Address es la direccion del buzon (la de la
// sesion): con ella sus companeros pueden pedir su disponibilidad y es el dueno de su pagina de citas.
type MailboxRef struct {
	TenantID  string
	MailboxID string
	Address   string
}

// resourceIDPattern es el nombre de un recurso de la libreta o del calendario sin extension, tal
// como lo genera mail-dav o lo trae un cliente CardDAV o CalDAV. Viaja en la ruta interna.
var resourceIDPattern = regexp.MustCompile(`^[A-Za-z0-9._@=+~-]{1,255}$`)

// ValidateResourceID rechaza lo que no puede ser el id de un contacto o de un evento.
func ValidateResourceID(id string) error {
	if !resourceIDPattern.MatchString(id) || id == "." || id == ".." {
		return invalid("id", "identificador inválido")
	}
	return nil
}

// ifMatchPattern es un ETag (RFC 9110 8.8.3) o una lista de ellos, en ASCII visible: viaja tal cual
// en la cabecera If-Match de la llamada a mail-dav.
var ifMatchPattern = regexp.MustCompile(`^[!-~][ -~]{0,255}$`)

// ValidateIfMatch admite vacio (sin condicion) o un valor de If-Match bien formado.
func ValidateIfMatch(v string) error {
	if v != "" && !ifMatchPattern.MatchString(v) {
		return invalid("if_match", "cabecera If-Match inválida")
	}
	return nil
}

// ContactValue es un correo o un telefono con su tipo (home, work, mobile u other).
type ContactValue struct {
	Value string
	Type  string
}

// ContactInput es un contacto tal como lo edita el usuario. Lo valida mail-dav.
type ContactInput struct {
	Name         string
	GivenName    string
	FamilyName   string
	Emails       []ContactValue
	Phones       []ContactValue
	Organization string
	Title        string
	Notes        string
	Birthday     string
}

// Contact es un contacto de la libreta personal del buzon.
type Contact struct {
	ContactInput
	ID        string
	ETag      string
	UpdatedAt *time.Time
}

// ContactPage es una pagina de la libreta.
type ContactPage struct {
	Items   []Contact
	Total   int64
	Page    int
	PerPage int
}

// ContactQuery es la busqueda y la pagina pedidas; los topes los aplica mail-dav.
type ContactQuery struct {
	Search  string
	Page    int
	PerPage int
}

// ImportSkip es una tarjeta del fichero que no se importo y por que.
type ImportSkip struct {
	Index  int
	Reason string
}

// ImportResult es el desenlace de importar un fichero vCard.
type ImportResult struct {
	Imported int
	Updated  int
	Skipped  []ImportSkip
}

// Recurrence es la repeticion simple de un evento.
type Recurrence struct {
	Freq     string
	Interval int
	Count    *int
	Until    *string
	ByDay    []string
}

// EventInput es un evento tal como lo edita el usuario. Start y End se transportan como llegan
// (fecha y hora RFC 3339, o fecha en un evento de dia completo): los interpreta mail-dav.
type EventInput struct {
	Title           string
	Start           string
	End             string
	AllDay          bool
	TimeZone        string
	Location        string
	Description     string
	Recurrence      *Recurrence
	ReminderMinutes *int
	Organizer       *Party
	Attendees       []Attendee
}

// Event es un evento del calendario personal del buzon.
type Event struct {
	EventInput
	ID   string
	ETag string
}

// Occurrence es una aparicion de un evento en una ventana de tiempo.
type Occurrence struct {
	ID           string
	Start        string
	End          string
	AllDay       bool
	Title        string
	Location     string
	Recurring    bool
	RecurrenceID string
}

// EventWindow es la ventana de la agenda; su tope lo aplica mail-dav.
type EventWindow struct {
	Start time.Time
	End   time.Time
}

// NewEventWindow interpreta la ventana (RFC 3339) y exige que termine despues de empezar.
func NewEventWindow(start, end string) (EventWindow, error) {
	s, err := time.Parse(time.RFC3339, start)
	if err != nil {
		return EventWindow{}, invalid("start", "debe ser una fecha y hora RFC 3339")
	}
	e, err := time.Parse(time.RFC3339, end)
	if err != nil {
		return EventWindow{}, invalid("end", "debe ser una fecha y hora RFC 3339")
	}
	if !e.After(s) {
		return EventWindow{}, invalid("end", "debe ser posterior a start")
	}
	return EventWindow{Start: s, End: e}, nil
}
