package domain

import "time"

// Window es una ventana del limite de tasa.
type Window int

const (
	WindowNone Window = iota
	WindowHour
	WindowDay
)

// RateOutcome es el resultado de intentar reservar cupo en las ventanas de hora y dia.
type RateOutcome struct {
	Allowed  bool
	HourUsed int64
	DayUsed  int64
	// Exceeded es la ventana sin cupo cuando Allowed es false.
	Exceeded Window
}

// Fits dice si count cabe en la ventana agotada cuando esta empiece de cero. Si no cabe
// ni vacia, esperar no sirve y no se sugiere reintento.
func (l Limits) Fits(count int64, w Window) bool {
	switch w {
	case WindowHour:
		return count <= l.Hourly
	case WindowDay:
		return count <= l.Daily
	}
	return true
}

// RetryAfter son los segundos hasta que abre la siguiente ventana del tipo agotado (hora o
// dia UTC). Nunca menos de 1: un cero invitaria a reintentar en el acto.
func RetryAfter(now time.Time, w Window) int64 {
	now = now.UTC()
	var next time.Time
	switch w {
	case WindowHour:
		next = now.Truncate(time.Hour).Add(time.Hour)
	case WindowDay:
		y, m, d := now.Date()
		next = time.Date(y, m, d+1, 0, 0, 0, 0, time.UTC)
	default:
		return 0
	}
	secs := int64((next.Sub(now) + time.Second - 1) / time.Second)
	if secs < 1 {
		secs = 1
	}
	return secs
}
