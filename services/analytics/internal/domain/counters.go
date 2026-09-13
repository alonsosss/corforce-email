package domain

import (
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

// ReportTimezone es la zona de los dias de todos los agregados. El panel convierte a la
// zona del usuario; la zona horaria por empresa queda pendiente.
const ReportTimezone = "UTC"

// Counter identifica cada contador de los agregados diarios.
type Counter int

const (
	CounterSent Counter = iota
	CounterDelivered
	CounterBouncedHard
	CounterBouncedSoft
	CounterComplained
	CounterOpenedUnique
	CounterClickedUnique
	CounterUnsubscribed
	CounterFailed
)

// CounterCount es el numero de contadores de un agregado.
const CounterCount = 9

// Counters son los contadores de envio de un dia o de un rango. Viajan como enteros JSON;
// las tasas, como texto decimal.
type Counters struct {
	Sent          int64 `json:"sent"`
	Delivered     int64 `json:"delivered"`
	BouncedHard   int64 `json:"bounced_hard"`
	BouncedSoft   int64 `json:"bounced_soft"`
	Complained    int64 `json:"complained"`
	OpenedUnique  int64 `json:"opened_unique"`
	ClickedUnique int64 `json:"clicked_unique"`
	Unsubscribed  int64 `json:"unsubscribed"`
	Failed        int64 `json:"failed"`
}

// Values devuelve los contadores en el orden de Counter, que es tambien el orden de las
// columnas de los agregados.
func (c Counters) Values() [CounterCount]int64 {
	return [CounterCount]int64{
		c.Sent, c.Delivered, c.BouncedHard, c.BouncedSoft, c.Complained,
		c.OpenedUnique, c.ClickedUnique, c.Unsubscribed, c.Failed,
	}
}

// Add suma delta al contador indicado.
func (c *Counters) Add(counter Counter, delta int64) {
	switch counter {
	case CounterSent:
		c.Sent += delta
	case CounterDelivered:
		c.Delivered += delta
	case CounterBouncedHard:
		c.BouncedHard += delta
	case CounterBouncedSoft:
		c.BouncedSoft += delta
	case CounterComplained:
		c.Complained += delta
	case CounterOpenedUnique:
		c.OpenedUnique += delta
	case CounterClickedUnique:
		c.ClickedUnique += delta
	case CounterUnsubscribed:
		c.Unsubscribed += delta
	case CounterFailed:
		c.Failed += delta
	}
}

// NonNegative indica si ningun contador resta.
func (c Counters) NonNegative() bool {
	for _, v := range c.Values() {
		if v < 0 {
			return false
		}
	}
	return true
}

// IsZero indica si todos los contadores son cero.
func (c Counters) IsZero() bool { return c == Counters{} }

// ratePlaces es la precision de las tasas que ve el panel.
const ratePlaces = 4

// Rates son las tasas del panel como texto decimal con cuatro decimales.
type Rates struct {
	Delivery    string `json:"delivery"`
	Bounce      string `json:"bounce"`
	Complaint   string `json:"complaint"`
	Open        string `json:"open"`
	Click       string `json:"click"`
	Unsubscribe string `json:"unsubscribe"`
}

// Rates calcula las tasas. Entrega y rebote se miden sobre lo enviado; queja, apertura,
// clic y baja sobre lo entregado, que es lo que el destinatario llego a recibir. Un
// denominador cero da "0.0000": nunca NaN ni Inf.
func (c Counters) Rates() Rates {
	return Rates{
		Delivery:    ratio(c.Delivered, c.Sent),
		Bounce:      ratio(c.BouncedHard+c.BouncedSoft, c.Sent),
		Complaint:   ratio(c.Complained, c.Delivered),
		Open:        ratio(c.OpenedUnique, c.Delivered),
		Click:       ratio(c.ClickedUnique, c.Delivered),
		Unsubscribe: ratio(c.Unsubscribed, c.Delivered),
	}
}

func ratio(num, den int64) string {
	if den <= 0 || num <= 0 {
		return decimal.Zero.StringFixed(ratePlaces)
	}
	return decimal.NewFromInt(num).DivRound(decimal.NewFromInt(den), ratePlaces).StringFixed(ratePlaces)
}

// DayCounters son los contadores de un dia UTC.
type DayCounters struct {
	Day      time.Time
	Counters Counters
}

// CounterDelta es el cambio de un contador en un dia.
type CounterDelta struct {
	Day     time.Time
	Counter Counter
	Delta   int64
}

// GroupByDay suma los deltas por dia, descarta los dias que quedan en cero y los devuelve
// en orden ascendente. Ese orden es el de bloqueo de las filas de los agregados: dos
// transacciones que tocan los mismos dias los bloquean en la misma secuencia y no pueden
// interbloquearse.
func GroupByDay(deltas []CounterDelta) []DayCounters {
	byDay := make(map[time.Time]*Counters, len(deltas))
	for _, d := range deltas {
		day := Day(d.Day)
		c, ok := byDay[day]
		if !ok {
			c = &Counters{}
			byDay[day] = c
		}
		c.Add(d.Counter, d.Delta)
	}
	out := make([]DayCounters, 0, len(byDay))
	for day, c := range byDay {
		if !c.IsZero() {
			out = append(out, DayCounters{Day: day, Counters: *c})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Day.Before(out[j].Day) })
	return out
}

// Day trunca un instante a su dia UTC.
func Day(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}
