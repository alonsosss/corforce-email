package domain

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	// MaxRangeDays es el rango maximo de una consulta: un ano, bisiesto incluido.
	MaxRangeDays = 366
	// DefaultRangeDays es el rango de una consulta sin fechas: los ultimos 30 dias.
	DefaultRangeDays = 30

	// DefaultDomainLimit y MaxDomainLimit acotan el ranking de dominios destino.
	DefaultDomainLimit = 20
	MaxDomainLimit     = 100

	dateLayout = "2006-01-02"
)

// Range es un rango de dias UTC, ambos extremos incluidos.
type Range struct {
	From time.Time
	To   time.Time
}

// Days es el numero de dias del rango.
func (r Range) Days() int { return int(r.To.Sub(r.From).Hours()/24) + 1 }

// ClassQuery es un rango con el filtro opcional de clase (vacio = todas).
type ClassQuery struct {
	Range Range
	Class Class
}

// ParseRange lee from y to (YYYY-MM-DD). Sin to, hasta hoy; sin from, los
// DefaultRangeDays dias que terminan en to. from no puede ser posterior a to y el rango no
// puede pasar de MaxRangeDays dias.
func ParseRange(from, to string, now time.Time) (Range, error) {
	var r Range
	var err error
	if to == "" {
		r.To = Day(now)
	} else if r.To, err = parseDate("to", to); err != nil {
		return Range{}, err
	}
	if from == "" {
		r.From = r.To.AddDate(0, 0, -(DefaultRangeDays - 1))
	} else if r.From, err = parseDate("from", from); err != nil {
		return Range{}, err
	}
	if r.From.After(r.To) {
		return Range{}, &ValidationError{Field: "from", Message: "no puede ser posterior a to"}
	}
	if r.Days() > MaxRangeDays {
		return Range{}, &ValidationError{Field: "to", Message: fmt.Sprintf("el rango admite como maximo %d dias", MaxRangeDays)}
	}
	return r, nil
}

// CampaignRange es el rango por defecto de la serie de una campana: sus dias con envios;
// si aun no los hay, de su inicio a su fin (o a hoy). Se recorta a los ultimos
// MaxRangeDays dias.
func CampaignRange(s CampaignSummary, now time.Time) Range {
	today := Day(now)
	r := Range{From: today, To: today}
	switch {
	case s.FirstDay != nil && s.LastDay != nil:
		r = Range{From: Day(*s.FirstDay), To: Day(*s.LastDay)}
	case s.StartedAt != nil:
		r.From = Day(*s.StartedAt)
		if s.CompletedAt != nil {
			r.To = Day(*s.CompletedAt)
		}
	}
	if r.From.After(r.To) {
		r.From = r.To
	}
	if r.Days() > MaxRangeDays {
		r.From = r.To.AddDate(0, 0, -(MaxRangeDays - 1))
	}
	return r
}

// ParseDomainLimit lee el tope del ranking de dominios: 1..MaxDomainLimit, por defecto
// DefaultDomainLimit.
func ParseDomainLimit(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return DefaultDomainLimit, nil
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < 1 || v > MaxDomainLimit {
		return 0, &ValidationError{Field: "limit", Message: fmt.Sprintf("debe ser un entero entre 1 y %d", MaxDomainLimit)}
	}
	return v, nil
}

// FormatDate escribe un dia como YYYY-MM-DD.
func FormatDate(t time.Time) string { return t.UTC().Format(dateLayout) }

func parseDate(field, s string) (time.Time, error) {
	t, err := time.ParseInLocation(dateLayout, s, time.UTC)
	if err != nil {
		return time.Time{}, &ValidationError{Field: field, Message: "debe tener el formato YYYY-MM-DD"}
	}
	return t, nil
}
