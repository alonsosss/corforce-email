package domain

import (
	"errors"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Budget acota el trabajo de una expansion de recurrencias: cada periodo recorrido y cada aparicion
// generada gastan una unidad. Un Budget nulo no acota nada. Un evento hostil (una regla que nunca
// coincide, o que genera millones de apariciones) agota el presupuesto en vez del tiempo del servidor.
type Budget struct {
	remaining int
	floor     int
}

func NewBudget(units int) *Budget { return &Budget{remaining: units} }

// Spend descuenta n unidades y dice si alcanzaron. Al fallar deja el presupuesto en su suelo.
func (b *Budget) Spend(n int) bool {
	if b == nil {
		return true
	}
	if b.remaining-n < b.floor {
		b.remaining = b.floor
		return false
	}
	b.remaining -= n
	return true
}

// Cap limita a n unidades lo que gasta lo que sigue (un evento) sin pasar del presupuesto total; release
// devuelve el limite anterior.
func (b *Budget) Cap(n int) (release func()) {
	if b == nil {
		return func() {}
	}
	old := b.floor
	if floor := b.remaining - n; floor > old {
		b.floor = floor
	}
	return func() { b.floor = old }
}

// ExpandResult dice como termino una expansion.
type ExpandResult int

const (
	// ExpandDone: la regla se agoto (COUNT, UNTIL o fin del calendario).
	ExpandDone ExpandResult = iota
	// ExpandStopped: quien la recorria dejo de pedir apariciones.
	ExpandStopped
	// ExpandIncomplete: la regla no se pudo evaluar entera (parte no soportada o presupuesto agotado).
	ExpandIncomplete
)

type weekdayRule struct {
	N   int
	Day time.Weekday
}

// RRule es una regla de recurrencia de RFC 5545 ya validada. Unsupported marca lo que el servidor no
// sabe expandir (frecuencias menores que un dia, BYWEEKNO, partes de otras extensiones): la regla se
// guarda igual, pero una consulta por rango no puede descartar sus apariciones.
type RRule struct {
	Freq        string
	Interval    int
	Count       int
	Until       *dtValue
	ByMonth     []int
	ByMonthDay  []int
	ByYearDay   []int
	BySetPos    []int
	ByHour      []int
	ByMinute    []int
	BySecond    []int
	ByDay       []weekdayRule
	WeekStart   time.Weekday
	Unsupported bool
}

const (
	maxRuleListLength = 400
	maxRuleInterval   = 1000
	maxRuleCount      = 1_000_000
	maxCalendarYear   = 9999
)

var (
	byDayRe    = regexp.MustCompile(`^([+-]?[0-9]{1,2})?(MO|TU|WE|TH|FR|SA|SU)$`)
	ruleKeyRe  = regexp.MustCompile(`^[A-Z][A-Z0-9-]*$`)
	weekdayIdx = map[string]time.Weekday{"SU": time.Sunday, "MO": time.Monday, "TU": time.Tuesday, "WE": time.Wednesday, "TH": time.Thursday, "FR": time.Friday, "SA": time.Saturday}
)

func parseIntList(v string, lo, hi int, allowZero bool) ([]int, error) {
	parts := strings.Split(v, ",")
	if len(parts) > maxRuleListLength {
		return nil, errors.New("lista demasiado larga en la regla de recurrencia")
	}
	seen := map[int]bool{}
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil || n < lo || n > hi || (n == 0 && !allowZero) {
			return nil, errors.New("valor fuera de rango en la regla de recurrencia")
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Ints(out)
	return out, nil
}

// ParseRRule valida el valor de una propiedad RRULE. Lo mal formado es un error; lo bien formado pero
// que no se sabe expandir se acepta y queda marcado (Unsupported).
func ParseRRule(value string) (RRule, error) {
	r := RRule{Interval: 1, WeekStart: time.Monday}
	seen := map[string]bool{}
	for _, part := range strings.Split(value, ";") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		key, val, ok := strings.Cut(part, "=")
		key, val = strings.ToUpper(strings.TrimSpace(key)), strings.TrimSpace(val)
		if !ok || val == "" || !ruleKeyRe.MatchString(key) {
			return RRule{}, errors.New("regla de recurrencia mal formada")
		}
		if seen[key] {
			return RRule{}, errors.New("parte repetida en la regla de recurrencia")
		}
		seen[key] = true
		var err error
		switch key {
		case "FREQ":
			f := strings.ToUpper(val)
			switch f {
			case "DAILY", "WEEKLY", "MONTHLY", "YEARLY":
			case "HOURLY", "MINUTELY", "SECONDLY":
				r.Unsupported = true
			default:
				return RRule{}, errors.New("FREQ no válida")
			}
			r.Freq = f
		case "INTERVAL":
			if r.Interval, err = strconv.Atoi(val); err != nil || r.Interval < 1 || r.Interval > maxRuleInterval {
				return RRule{}, errors.New("INTERVAL fuera de rango")
			}
		case "COUNT":
			if r.Count, err = strconv.Atoi(val); err != nil || r.Count < 1 || r.Count > maxRuleCount {
				return RRule{}, errors.New("COUNT fuera de rango")
			}
		case "UNTIL":
			u, err := parseDateTime(val, nil)
			if err != nil || u.tzid != "" {
				return RRule{}, errors.New("UNTIL no válido")
			}
			r.Until = &u
		case "WKST":
			wd, ok := weekdayIdx[strings.ToUpper(val)]
			if !ok {
				return RRule{}, errors.New("WKST no válido")
			}
			r.WeekStart = wd
		case "BYMONTH":
			r.ByMonth, err = parseIntList(val, 1, 12, false)
		case "BYMONTHDAY":
			r.ByMonthDay, err = parseIntList(val, -31, 31, false)
		case "BYYEARDAY":
			r.ByYearDay, err = parseIntList(val, -366, 366, false)
		case "BYSETPOS":
			r.BySetPos, err = parseIntList(val, -366, 366, false)
		case "BYHOUR":
			r.ByHour, err = parseIntList(val, 0, 23, true)
		case "BYMINUTE":
			r.ByMinute, err = parseIntList(val, 0, 59, true)
		case "BYSECOND":
			r.BySecond, err = parseIntList(val, 0, 59, true)
		case "BYWEEKNO":
			_, err = parseIntList(val, -53, 53, false)
			r.Unsupported = true
		case "BYDAY":
			r.ByDay, err = parseByDay(val)
		default:
			r.Unsupported = true
		}
		if err != nil {
			return RRule{}, err
		}
	}
	if r.Freq == "" {
		return RRule{}, errors.New("la regla de recurrencia no tiene FREQ")
	}
	if r.Count > 0 && r.Until != nil {
		return RRule{}, errors.New("COUNT y UNTIL no pueden ir juntos")
	}
	for _, d := range r.ByDay {
		if d.N != 0 && r.Freq != "MONTHLY" && r.Freq != "YEARLY" {
			return RRule{}, errors.New("BYDAY con ordinal solo vale con FREQ MONTHLY o YEARLY")
		}
	}
	if len(r.ByYearDay) > 0 && r.Freq != "YEARLY" {
		r.Unsupported = true
	}
	return r, nil
}

func parseByDay(v string) ([]weekdayRule, error) {
	parts := strings.Split(v, ",")
	if len(parts) > maxRuleListLength {
		return nil, errors.New("lista demasiado larga en la regla de recurrencia")
	}
	out := make([]weekdayRule, 0, len(parts))
	for _, p := range parts {
		m := byDayRe.FindStringSubmatch(strings.ToUpper(strings.TrimSpace(p)))
		if m == nil {
			return nil, errors.New("BYDAY no válido")
		}
		n := 0
		if m[1] != "" {
			n, _ = strconv.Atoi(m[1])
			if n == 0 || n > 53 || n < -53 {
				return nil, errors.New("BYDAY con ordinal fuera de rango")
			}
		}
		out = append(out, weekdayRule{N: n, Day: weekdayIdx[m[2]]})
	}
	return out, nil
}

func contains1(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func midnight(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func daysInMonth(t time.Time) int {
	return time.Date(t.Year(), t.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

func daysInYear(t time.Time) int {
	return time.Date(t.Year(), 12, 31, 0, 0, 0, 0, time.UTC).YearDay()
}

// effective aplica los valores por omision de RFC 5545 (los que toma de DTSTART cuando la regla no dice
// que dia es).
func (r RRule) effective(dt time.Time) (byMonth, byMonthDay []int, byDay []weekdayRule) {
	byMonth, byMonthDay, byDay = r.ByMonth, r.ByMonthDay, r.ByDay
	if len(r.ByYearDay) > 0 || len(byMonthDay) > 0 || len(byDay) > 0 {
		return
	}
	switch r.Freq {
	case "YEARLY":
		if len(byMonth) == 0 {
			byMonth = []int{int(dt.Month())}
		}
		byMonthDay = []int{dt.Day()}
	case "MONTHLY":
		byMonthDay = []int{dt.Day()}
	case "WEEKLY":
		byDay = []weekdayRule{{Day: dt.Weekday()}}
	}
	return
}

func (r RRule) matchDay(d time.Time, byMonth, byMonthDay []int, byDay []weekdayRule) bool {
	if len(byMonth) > 0 && !contains1(byMonth, int(d.Month())) {
		return false
	}
	if len(byMonthDay) > 0 && !contains1(byMonthDay, d.Day()) && !contains1(byMonthDay, d.Day()-daysInMonth(d)-1) {
		return false
	}
	if len(r.ByYearDay) > 0 && !contains1(r.ByYearDay, d.YearDay()) && !contains1(r.ByYearDay, d.YearDay()-daysInYear(d)-1) {
		return false
	}
	if len(byDay) == 0 {
		return true
	}
	inMonth := r.Freq == "MONTHLY" || (r.Freq == "YEARLY" && len(byMonth) > 0)
	for _, w := range byDay {
		if w.Day != d.Weekday() {
			continue
		}
		if w.N == 0 {
			return true
		}
		day, length := d.YearDay(), daysInYear(d)
		if inMonth {
			day, length = d.Day(), daysInMonth(d)
		}
		if w.N == (day-1)/7+1 || w.N == -((length-day)/7+1) {
			return true
		}
	}
	return false
}

// periodStart es el primer dia del periodo i (contado desde el de DTSTART, de interval en interval). Falla
// pasado el ultimo anio que admite iCalendar.
func (r RRule) periodStart(dt time.Time, i int64) (time.Time, bool) {
	step := i * int64(r.Interval)
	switch r.Freq {
	case "DAILY":
		ps := midnight(dt).AddDate(0, 0, int(step))
		return ps, ps.Year() <= maxCalendarYear
	case "WEEKLY":
		ps := r.weekStart(midnight(dt)).AddDate(0, 0, int(step)*7)
		return ps, ps.Year() <= maxCalendarYear
	case "MONTHLY":
		months := int64(dt.Year())*12 + int64(dt.Month()) - 1 + step
		if months/12 > maxCalendarYear {
			return time.Time{}, false
		}
		return time.Date(int(months/12), time.Month(months%12+1), 1, 0, 0, 0, 0, time.UTC), true
	default:
		year := int64(dt.Year()) + step
		if year > maxCalendarYear {
			return time.Time{}, false
		}
		return time.Date(int(year), 1, 1, 0, 0, 0, 0, time.UTC), true
	}
}

func (r RRule) weekStart(day time.Time) time.Time {
	back := (int(day.Weekday()) - int(r.WeekStart) + 7) % 7
	return day.AddDate(0, 0, -back)
}

// firstPeriod salta los periodos que terminan antes de from. Solo es valido sin COUNT: con COUNT hay que
// contar desde la primera aparicion.
func (r RRule) firstPeriod(dt, from time.Time) int64 {
	if r.Count > 0 || from.IsZero() || !from.After(dt) {
		return 0
	}
	var dist int64
	switch r.Freq {
	case "DAILY":
		dist = int64(midnight(from).Sub(midnight(dt)) / (24 * time.Hour))
	case "WEEKLY":
		dist = int64(r.weekStart(midnight(from)).Sub(r.weekStart(midnight(dt))) / (7 * 24 * time.Hour))
	case "MONTHLY":
		dist = int64(from.Year()-dt.Year())*12 + int64(from.Month()) - int64(dt.Month())
	default:
		dist = int64(from.Year() - dt.Year())
	}
	if idx := dist/int64(r.Interval) - 1; idx > 0 {
		return idx
	}
	return 0
}

// days son los dias del periodo que cumplen las partes BYxxx de la regla, en orden.
func (r RRule) days(ps time.Time, byMonth, byMonthDay []int, byDay []weekdayRule) []time.Time {
	var out []time.Time
	add := func(d time.Time) {
		if r.matchDay(d, byMonth, byMonthDay, byDay) {
			out = append(out, d)
		}
	}
	month := func(first time.Time) {
		for d := first; d.Month() == first.Month(); d = d.AddDate(0, 0, 1) {
			add(d)
		}
	}
	switch r.Freq {
	case "DAILY":
		add(ps)
	case "WEEKLY":
		for k := 0; k < 7; k++ {
			add(ps.AddDate(0, 0, k))
		}
	case "MONTHLY":
		month(ps)
	default:
		for m := 1; m <= 12; m++ {
			if len(byMonth) == 0 || contains1(byMonth, m) {
				month(time.Date(ps.Year(), time.Month(m), 1, 0, 0, 0, 0, time.UTC))
			}
		}
	}
	return out
}

// timesOfDay son los segundos desde medianoche de cada hora del dia que produce la regla.
func (r RRule) timesOfDay(dt time.Time) []int {
	pick := func(list []int, def int) []int {
		if len(list) == 0 {
			return []int{def}
		}
		return list
	}
	var out []int
	for _, h := range pick(r.ByHour, dt.Hour()) {
		for _, m := range pick(r.ByMinute, dt.Minute()) {
			for _, s := range pick(r.BySecond, dt.Second()) {
				out = append(out, h*3600+m*60+s)
			}
		}
	}
	sort.Ints(out)
	return out
}

func (r RRule) untilPassed(c time.Time, toInstant func(time.Time) time.Time) bool {
	if r.Until == nil {
		return false
	}
	u := r.Until.wall
	if r.Until.date {
		u = u.Add(24*time.Hour - time.Nanosecond)
	}
	if r.Until.utc {
		return toInstant(c).After(u)
	}
	return c.After(u)
}

// Each recorre en orden las apariciones de la regla desde start (relojes de pared) y llama a fn con cada
// una; fn devuelve false para dejar de pedir. toInstant convierte un reloj de pared en instante y sirve
// para comparar con un UNTIL en UTC. from, si no es cero, permite saltar los periodos anteriores (solo sin
// COUNT). El trabajo se descuenta de b: al agotarse, o ante una regla que no se sabe expandir, devuelve
// ExpandIncomplete y quien llama no debe dar por descartada ninguna aparicion.
func (r RRule) Each(start dtValue, toInstant func(time.Time) time.Time, b *Budget, from time.Time, fn func(wall time.Time) bool) ExpandResult {
	if r.Unsupported {
		return ExpandIncomplete
	}
	dt := start.wall
	byMonth, byMonthDay, byDay := r.effective(dt)
	times := r.timesOfDay(dt)
	emitted := 0
	for i := r.firstPeriod(dt, from); ; i++ {
		if !b.Spend(1) {
			return ExpandIncomplete
		}
		ps, ok := r.periodStart(dt, i)
		if !ok {
			return ExpandDone
		}
		if r.Until != nil && ps.After(r.Until.wall.Add(48*time.Hour)) {
			return ExpandDone
		}
		days := r.days(ps, byMonth, byMonthDay, byDay)
		if !b.Spend(len(days) * len(times)) {
			return ExpandIncomplete
		}
		cands := make([]time.Time, 0, len(days)*len(times))
		for _, d := range days {
			for _, s := range times {
				cands = append(cands, d.Add(time.Duration(s)*time.Second))
			}
		}
		sort.Slice(cands, func(a, c int) bool { return cands[a].Before(cands[c]) })
		if len(r.BySetPos) > 0 {
			cands = pickPositions(cands, r.BySetPos)
		}
		for _, c := range cands {
			if c.Before(dt) {
				continue
			}
			if r.untilPassed(c, toInstant) {
				return ExpandDone
			}
			emitted++
			if r.Count > 0 && emitted > r.Count {
				return ExpandDone
			}
			if !fn(c) {
				return ExpandStopped
			}
			if r.Count > 0 && emitted == r.Count {
				return ExpandDone
			}
		}
	}
}

func pickPositions(cands []time.Time, positions []int) []time.Time {
	chosen := map[int]bool{}
	for _, p := range positions {
		idx := p - 1
		if p < 0 {
			idx = len(cands) + p
		}
		if idx >= 0 && idx < len(cands) {
			chosen[idx] = true
		}
	}
	out := make([]time.Time, 0, len(chosen))
	for i, c := range cands {
		if chosen[i] {
			out = append(out, c)
		}
	}
	return out
}
