package domain

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Limites de la configuracion de una pagina de citas. Acotan la forma de la pagina, no el volumen: el tope diario
// de reservas lo decide su dueno por debajo del techo del operador (MAIL_DAV_BOOKING_MAX_DAILY).
const (
	MaxBookingTitleRunes       = 200
	MaxBookingDescriptionRunes = 2000
	MinBookingDuration         = 5
	MaxBookingDuration         = 8 * 60
	MaxBookingBuffer           = 4 * 60
	MaxBookingNotice           = 30 * 24 * 60
	MaxBookingAdvanceDays      = 365
	MaxBookingWindowsPerDay    = 6
	// MaxBookingNoteRunes y MaxBookingNameRunes acotan lo que escribe el visitante.
	MaxBookingNoteRunes = 2000
	MaxBookingNameRunes = 200
)

var (
	publicIDRe  = regexp.MustCompile(`^[A-Za-z0-9_-]{22,64}$`)
	clockTimeRe = regexp.MustCompile(`^([01][0-9]|2[0-3]):([0-5][0-9])$|^24:00$`)
	dayCodes    = [...]string{time.Sunday: "SU", time.Monday: "MO", time.Tuesday: "TU", time.Wednesday: "WE", time.Thursday: "TH", time.Friday: "FR", time.Saturday: "SA"}
)

// ValidPublicID dice si s tiene la forma del identificador publico de una pagina de citas (el segmento del
// enlace), que genera el servicio con 128 bits aleatorios o mas.
func ValidPublicID(s string) bool { return publicIDRe.MatchString(s) }

// DayWindow es una franja de un dia de la semana en minutos desde la medianoche, [Start, End).
type DayWindow struct {
	Start int
	End   int
}

// FormatClock escribe minutos desde la medianoche como HH:MM.
func FormatClock(m int) string { return fmt.Sprintf("%02d:%02d", m/60, m%60) }

// ParseClock lee HH:MM (00:00 a 24:00).
func ParseClock(field, v string) (int, error) {
	v = strings.TrimSpace(v)
	if !clockTimeRe.MatchString(v) {
		return 0, fieldError(field, "se espera una hora HH:MM")
	}
	h, _ := strconv.Atoi(v[:2])
	m, _ := strconv.Atoi(v[3:])
	return h*60 + m, nil
}

// WeekdayCode es el codigo de dos letras (MO..SU) de un dia de la semana.
func WeekdayCode(d time.Weekday) string { return dayCodes[d] }

// WeekdayOf lee un codigo MO..SU.
func WeekdayOf(code string) (time.Weekday, bool) {
	d, ok := weekdayIdx[strings.ToUpper(strings.TrimSpace(code))]
	return d, ok
}

// BookingSettings es lo que el dueno configura de su pagina de citas.
type BookingSettings struct {
	Title            string
	Description      string
	DurationMinutes  int
	BufferMinutes    int
	MinNoticeMinutes int
	MaxAdvanceDays   int
	DailyLimit       int
	TimeZone         string
	Weekly           [7][]DayWindow
	Active           bool
}

// BookingPage es la pagina de citas de un buzon: su configuracion, el identificador de su enlace publico y el
// dueno al que se invita (su direccion y su nombre, que pone el webmail con los de la sesion).
type BookingPage struct {
	BookingSettings
	ID           uuid.UUID
	TenantID     uuid.UUID
	MailboxID    uuid.UUID
	PublicID     string
	OwnerAddress string
	OwnerName    string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func intRange(field string, v, lo, hi int) error {
	if v < lo || v > hi {
		return fieldError(field, fmt.Sprintf("se espera un entero entre %d y %d", lo, hi))
	}
	return nil
}

// Normalize valida la configuracion. maxDaily es el techo del operador para el tope diario de reservas.
func (s BookingSettings) Normalize(maxDaily int) (BookingSettings, error) {
	out := s
	out.Title = strings.TrimSpace(s.Title)
	out.Description = strings.TrimSpace(strings.ReplaceAll(s.Description, "\r\n", "\n"))
	if out.Title == "" {
		return BookingSettings{}, fieldError("title", "es obligatorio")
	}
	if err := validText("title", out.Title, MaxBookingTitleRunes, false); err != nil {
		return BookingSettings{}, err
	}
	if err := validText("description", out.Description, MaxBookingDescriptionRunes, true); err != nil {
		return BookingSettings{}, err
	}
	for _, c := range []struct {
		field     string
		v, lo, hi int
	}{
		{"duration_minutes", s.DurationMinutes, MinBookingDuration, MaxBookingDuration},
		{"buffer_minutes", s.BufferMinutes, 0, MaxBookingBuffer},
		{"min_notice_minutes", s.MinNoticeMinutes, 0, MaxBookingNotice},
		{"max_advance_days", s.MaxAdvanceDays, 1, MaxBookingAdvanceDays},
		{"daily_limit", s.DailyLimit, 1, maxDaily},
	} {
		if err := intRange(c.field, c.v, c.lo, c.hi); err != nil {
			return BookingSettings{}, err
		}
	}
	loc := ianaLocation(strings.TrimSpace(s.TimeZone))
	if loc == nil {
		return BookingSettings{}, fieldError("timezone", "no es una zona horaria IANA")
	}
	out.TimeZone = loc.String()
	windows := 0
	for d := range s.Weekly {
		day := append([]DayWindow(nil), s.Weekly[d]...)
		field := "weekly." + dayCodes[d]
		if len(day) > MaxBookingWindowsPerDay {
			return BookingSettings{}, fieldError(field, "demasiadas franjas en el dia")
		}
		sort.Slice(day, func(i, j int) bool { return day[i].Start < day[j].Start })
		for i, w := range day {
			if w.Start < 0 || w.End > 24*60 || w.End-w.Start < s.DurationMinutes {
				return BookingSettings{}, fieldError(field, "cada franja debe durar al menos una cita")
			}
			if i > 0 && w.Start < day[i-1].End {
				return BookingSettings{}, fieldError(field, "las franjas se solapan")
			}
		}
		out.Weekly[d] = day
		windows += len(day)
	}
	if out.Active && windows == 0 {
		return BookingSettings{}, fieldError("weekly", "una pagina activa necesita al menos una franja")
	}
	return out, nil
}

// Slots son los huecos libres de la pagina en [from, to): los de sus franjas (en su zona, cada uno de la duracion
// de la cita), a partir de la antelacion minima y hasta la maxima contadas desde now, que no chocan con busy
// (ensanchado por el margen entre citas). Como mucho max, en orden.
func (p BookingPage) Slots(from, to, now time.Time, busy []Interval, max int) []Interval {
	loc := ianaLocation(p.TimeZone)
	if loc == nil || max < 1 {
		return []Interval{}
	}
	if earliest := now.Add(time.Duration(p.MinNoticeMinutes) * time.Minute); from.Before(earliest) {
		from = earliest
	}
	if latest := now.AddDate(0, 0, p.MaxAdvanceDays); to.After(latest) {
		to = latest
	}
	out := []Interval{}
	if !to.After(from) {
		return out
	}
	dur := time.Duration(p.DurationMinutes) * time.Minute
	margin := time.Duration(p.BufferMinutes) * time.Minute
	blocked := MergeIntervals(busy)
	free := func(s, e time.Time) bool {
		for _, b := range blocked {
			if s.Before(b.End.Add(margin)) && e.Add(margin).After(b.Start) {
				return false
			}
		}
		return true
	}
	first := from.In(loc)
	day := time.Date(first.Year(), first.Month(), first.Day(), 0, 0, 0, 0, loc)
	for ; day.Before(to); day = time.Date(day.Year(), day.Month(), day.Day()+1, 0, 0, 0, 0, loc) {
		for _, w := range p.Weekly[day.Weekday()] {
			for m := w.Start; m+p.DurationMinutes <= w.End; m += p.DurationMinutes {
				s := time.Date(day.Year(), day.Month(), day.Day(), m/60, m%60, 0, 0, loc)
				e := s.Add(dur)
				if s.Before(from) || e.After(to) || !free(s, e) {
					continue
				}
				out = append(out, Interval{Start: s.UTC(), End: e.UTC()})
				if len(out) >= max {
					return out
				}
			}
		}
	}
	return out
}

// SlotAvailable dice si start es el inicio de un hueco libre de la pagina.
func (p BookingPage) SlotAvailable(start, now time.Time, busy []Interval) bool {
	slots := p.Slots(start, start.Add(time.Duration(p.DurationMinutes)*time.Minute), now, busy, 1)
	return len(slots) == 1 && slots[0].Start.Equal(start)
}

// BookingRequest es lo que envia el visitante al reservar.
type BookingRequest struct {
	Start time.Time
	Name  string
	Email string
	Note  string
}

// Normalize valida la reserva: la direccion del visitante, su nombre y su nota.
func (r BookingRequest) Normalize() (BookingRequest, error) {
	out := r
	var err error
	if out.Email, err = NormalizeAddress("email", r.Email); err != nil {
		return BookingRequest{}, err
	}
	if out.Name, err = NormalizePartyName("name", r.Name); err != nil {
		return BookingRequest{}, err
	}
	if out.Name == "" {
		return BookingRequest{}, fieldError("name", "es obligatorio")
	}
	if len([]rune(out.Name)) > MaxBookingNameRunes {
		return BookingRequest{}, fieldError("name", "supera el largo maximo")
	}
	out.Note = strings.TrimSpace(strings.ReplaceAll(r.Note, "\r\n", "\n"))
	if err := validText("note", out.Note, MaxBookingNoteRunes, true); err != nil {
		return BookingRequest{}, err
	}
	if out.Start.IsZero() {
		return BookingRequest{}, fieldError("start", "es obligatorio")
	}
	out.Start = out.Start.UTC().Truncate(time.Second)
	return out, nil
}

// BookingEventFields es el evento de una cita: el titulo de la pagina, las horas en su zona, la reserva en la
// descripcion (solo la ve el dueno: la invitacion al visitante no la lleva) y la reunion con el dueno como
// organizador que ya acepto y el visitante como invitado.
func (p BookingPage) BookingEventFields(r BookingRequest) EventFields {
	desc := "Reservado por: " + r.Name + " <" + r.Email + ">"
	if r.Note != "" {
		desc += "\n\n" + r.Note
	}
	owner := Attendee{Email: p.OwnerAddress, Name: p.OwnerName, PartStat: PartStatAccepted}
	return EventFields{
		Title: p.Title, Start: r.Start, End: r.Start.Add(time.Duration(p.DurationMinutes) * time.Minute),
		TimeZone: p.TimeZone, Description: desc,
		Organizer: &Party{Email: p.OwnerAddress, Name: p.OwnerName},
		Attendees: []Attendee{owner, {Email: r.Email, Name: r.Name, PartStat: PartStatNeedsAction}},
	}
}

// Booking es el registro minimo de una reserva, para los topes: la pagina, el evento creado, las horas y el
// SHA-256 del correo del visitante (con el id de la pagina delante, para que no sirva fuera de ella).
type Booking struct {
	PageID        uuid.UUID
	EventResource string
	Start         time.Time
	End           time.Time
	VisitorHash   string
}

// BookingLimits son los topes de una reserva: reservas de la pagina y del mismo visitante en 24 horas, el margen
// entre citas y los de escritura del calendario del dueno.
type BookingLimits struct {
	Daily      int
	PerVisitor int
	Buffer     time.Duration
	Write      WriteLimits
}
