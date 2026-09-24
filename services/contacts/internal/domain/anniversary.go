package domain

import (
	"errors"
	"time"
)

// Aniversarios de un atributo de fecha (cumpleanos, alta como cliente): el dia y el mes del
// valor, en la zona del contacto o en la de respaldo que indica el flujo de automations.
//
// Un 29 de febrero se celebra el 28 en los anos no bisiestos: es la fecha que la persona
// reconoce como suya en ese mes, y el 1 de marzo ya seria otro mes.

// MaxAnniversaryHour es la ultima hora local admitida (0..23).
const MaxAnniversaryHour = 23

var ErrInvalidAnniversary = errors.New("aniversario no válido: atributo de fecha declarado, hora entre 0 y 23 y zona IANA de respaldo")

// AnniversaryOccurrence dice si el aniversario del valor (AAAA-MM-DD) cae en la fecha
// local de now en loc y si ya es la hora hour o posterior. occurrence es esa fecha local
// (AAAA-MM-DD): identifica el aniversario de ese ano. Un valor que no es una fecha no
// cae nunca.
func AnniversaryOccurrence(value string, loc *time.Location, now time.Time, hour int) (occurrence string, ok bool) {
	d, err := time.Parse(DateLayout, value)
	if err != nil || loc == nil {
		return "", false
	}
	local := now.In(loc)
	if local.Hour() < hour {
		return "", false
	}
	if !sameAnniversary(d, local) {
		return "", false
	}
	return local.Format(DateLayout), true
}

func sameAnniversary(d, local time.Time) bool {
	if d.Month() == local.Month() && d.Day() == local.Day() {
		return true
	}
	return d.Month() == time.February && d.Day() == 29 &&
		local.Month() == time.February && local.Day() == 28 && !isLeap(local.Year())
}

func isLeap(year int) bool {
	return year%4 == 0 && (year%100 != 0 || year%400 == 0)
}

// AnniversaryCandidates son los "MM-DD" que pueden caer hoy en alguna zona del mundo: la
// fecha local de cualquier zona (UTC-12 a UTC+14) esta entre el dia anterior y el
// siguiente al de UTC. Sirve para que la base prefiltre por el valor sin calcular zonas;
// la decision final es AnniversaryOccurrence.
func AnniversaryCandidates(now time.Time) []string {
	utc := now.UTC()
	out := make([]string, 0, 4)
	for _, delta := range []int{-1, 0, 1} {
		day := utc.AddDate(0, 0, delta)
		out = append(out, day.Format("01-02"))
		if day.Month() == time.February && day.Day() == 28 && !isLeap(day.Year()) {
			out = append(out, "02-29")
		}
	}
	return out
}
