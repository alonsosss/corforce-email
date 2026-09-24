package domain

import (
	"strings"
	"time"
	"unicode/utf8"
)

// InvitationMail es un correo iMIP (RFC 6047): el texto para personas y el iCalendar con su METHOD, que el
// compositor pone en una parte text/calendar. Sale por el mismo camino que un correo normal (el submission de la
// celda, autenticado como el buzon del remitente).
type InvitationMail struct {
	From      Address
	To        []Address
	Cc        []Address
	Subject   string
	Text      string
	Method    string
	ICal      string
	MessageID string
	Date      time.Time
}

// Recipients son las direcciones del sobre, sin repetir.
func (m InvitationMail) Recipients() []string {
	var out []string
	seen := map[string]bool{}
	for _, list := range [][]Address{m.To, m.Cc} {
		for _, a := range list {
			if k := strings.ToLower(a.Email); !seen[k] {
				seen[k] = true
				out = append(out, a.Email)
			}
		}
	}
	return out
}

// InvitationKind es el motivo de un correo de calendario, que decide su asunto y su texto.
type InvitationKind string

const (
	InvitationRequest   InvitationKind = "request"
	InvitationCancel    InvitationKind = "cancel"
	InvitationAccepted  InvitationKind = "accepted"
	InvitationTentative InvitationKind = "tentative"
	InvitationDeclined  InvitationKind = "declined"
	InvitationBooking   InvitationKind = "booking"
)

// ReplyKind es el motivo del correo de una respuesta.
func ReplyKind(response string) InvitationKind {
	switch response {
	case PartStatAccepted:
		return InvitationAccepted
	case PartStatTentative:
		return InvitationTentative
	}
	return InvitationDeclined
}

// Textos de los correos de calendario. Son del producto, no de la interfaz: salen del servidor para que la pagina
// publica de citas no pueda decidir que se escribe en un correo enviado desde el buzon de una empresa.
var invitationSubjects = map[InvitationKind]string{
	InvitationRequest:   "Invitacion: ",
	InvitationCancel:    "Cancelada: ",
	InvitationAccepted:  "Aceptada: ",
	InvitationTentative: "Tentativa: ",
	InvitationDeclined:  "Rechazada: ",
	InvitationBooking:   "Cita confirmada: ",
}

var invitationLeads = map[InvitationKind]string{
	InvitationRequest:   "Te invitan a una reunion.",
	InvitationCancel:    "Esta reunion se ha cancelado.",
	InvitationAccepted:  "La invitacion ha sido aceptada.",
	InvitationTentative: "La invitacion ha sido aceptada de forma tentativa.",
	InvitationDeclined:  "La invitacion ha sido rechazada.",
	InvitationBooking:   "La cita esta confirmada.",
}

// InvitationSummary es lo que el texto del correo cuenta del evento.
type InvitationSummary struct {
	Title     string
	Start     string
	End       string
	AllDay    bool
	TimeZone  string
	Location  string
	Organizer string
	Attendee  string
}

// InvitationSubject es el asunto del correo, acotado como el de uno normal.
func InvitationSubject(kind InvitationKind, title string) string {
	s := invitationSubjects[kind] + strings.TrimSpace(title)
	for utf8.RuneCountInString(s) > MaxSubjectRunes {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	return s
}

// formatWhen escribe el intervalo en la zona del evento (o en UTC), con fechas y horas numericas.
func formatWhen(s InvitationSummary) string {
	start, err := time.Parse(time.RFC3339, s.Start)
	if err != nil {
		return ""
	}
	end, endErr := time.Parse(time.RFC3339, s.End)
	loc := time.UTC
	zone := "UTC"
	if s.TimeZone != "" && !s.AllDay {
		if l, err := time.LoadLocation(s.TimeZone); err == nil {
			loc, zone = l, s.TimeZone
		}
	}
	start = start.In(loc)
	if s.AllDay {
		out := start.Format("2006-01-02")
		if endErr == nil && end.Sub(start) > 24*time.Hour {
			out += " - " + end.In(loc).Add(-24*time.Hour).Format("2006-01-02")
		}
		return out
	}
	out := start.Format("2006-01-02 15:04")
	if endErr == nil {
		end = end.In(loc)
		if end.Format("2006-01-02") == start.Format("2006-01-02") {
			out += " - " + end.Format("15:04")
		} else {
			out += " - " + end.Format("2006-01-02 15:04")
		}
	}
	return out + " (" + zone + ")"
}

// InvitationText es el cuerpo en texto del correo.
func InvitationText(kind InvitationKind, s InvitationSummary) string {
	lines := []string{invitationLeads[kind], ""}
	add := func(label, value string) {
		if value = strings.TrimSpace(value); value != "" {
			lines = append(lines, label+value)
		}
	}
	add("Asunto: ", s.Title)
	add("Cuando: ", formatWhen(s))
	add("Lugar: ", s.Location)
	add("Organiza: ", s.Organizer)
	add("Responde: ", s.Attendee)
	return strings.Join(lines, "\n") + "\n"
}
