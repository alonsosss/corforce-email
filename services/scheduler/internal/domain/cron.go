package domain

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// MinCronEvery es el periodo minimo de @every. Es la resolucion de una expresion de cinco
// campos y el minimo de interval_minutes: un periodo menor no se cumpliria (el ticker pasa
// cada 30 s) y solo convertiria el trabajo en "en cada pasada", martilleando la base de la
// empresa y el ejecutor.
const MinCronEvery = time.Minute

// MaxCronExpressionLength es el ancho de job_definitions.cron_expression.
const MaxCronExpressionLength = 100

// Descriptores admitidos, ademas de "@every <duracion>". Es un contrato cerrado: la UI y la
// documentacion ofrecen exactamente estos.
var cronDescriptors = map[string]bool{"@hourly": true, "@daily": true, "@weekly": true, "@monthly": true}

const everyPrefix = "@every "

// cronParser admite los cinco campos estandar (minuto, hora, dia del mes, mes, dia de la
// semana) y los descriptores; sin campo de segundos.
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// neverProbe es la referencia con la que se comprueba que una expresion ocurre alguna vez.
// Cualquiera sirve: el patron de una expresion que ocurre se repite en menos de cinco anos,
// que es lo que busca la libreria antes de rendirse.
var neverProbe = time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC)

// CronSpec es una expresion cron ya validada con la zona del trabajo en la que se evalua.
type CronSpec struct {
	schedule cron.Schedule
	// every es el periodo de @every; cero para las expresiones de calendario.
	every time.Duration
	loc   *time.Location
}

// ParseCron valida una expresion (cinco campos o uno de los descriptores admitidos) y la
// zona IANA en la que se evalua.
func ParseCron(expr, timezone string) (CronSpec, error) {
	if len(expr) > MaxCronExpressionLength {
		return CronSpec{}, invalidCron("longer than %d characters", MaxCronExpressionLength)
	}
	spec := strings.TrimSpace(expr)
	if spec == "" {
		return CronSpec{}, invalidCron("cron_expression is required for cron jobs")
	}
	// La libreria aceptaria un prefijo TZ=/CRON_TZ= y cargaria la zona por su cuenta: la zona
	// no es parte de la expresion, es el campo timezone del trabajo.
	if strings.HasPrefix(spec, "TZ=") || strings.HasPrefix(spec, "CRON_TZ=") {
		return CronSpec{}, invalidCron("the time zone goes in the timezone field, not in the expression")
	}
	if strings.HasPrefix(spec, "@") && !cronDescriptors[spec] && !strings.HasPrefix(spec, everyPrefix) {
		return CronSpec{}, invalidCron("descriptor must be @hourly, @daily, @weekly, @monthly or @every <duration>")
	}
	schedule, err := cronParser.Parse(spec)
	if err != nil {
		return CronSpec{}, invalidCron("%s", err.Error())
	}
	var every time.Duration
	if delay, ok := schedule.(cron.ConstantDelaySchedule); ok {
		// La libreria sube a un segundo cualquier periodo menor, cero o negativo incluidos.
		if delay.Delay < MinCronEvery {
			return CronSpec{}, invalidCron("@every must be at least %s", MinCronEvery)
		}
		every = delay.Delay
	} else if schedule.Next(neverProbe).IsZero() {
		return CronSpec{}, invalidCron("the expression never matches a date")
	}
	loc, err := LoadTimezone(timezone)
	if err != nil {
		return CronSpec{}, err
	}
	return CronSpec{schedule: schedule, every: every, loc: loc}, nil
}

func invalidCron(format string, args ...any) error {
	return invalidField(FieldCronExpression, ErrInvalidCron, format, args...)
}

// CronDescriptors devuelve los descriptores admitidos, ademas de @every, ordenados.
func CronDescriptors() []string {
	out := make([]string, 0, len(cronDescriptors))
	for d := range cronDescriptors {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// NextRun es la primera ocurrencia de expr en la zona timezone estrictamente posterior a
// after, en UTC.
func NextRun(expr, timezone string, after time.Time) (time.Time, error) {
	spec, err := ParseCron(expr, timezone)
	if err != nil {
		return time.Time{}, err
	}
	return spec.Next(after)
}

// Next es la primera ocurrencia estrictamente posterior a after, en UTC.
//
// Una expresion de calendario describe horas de pared de la zona del trabajo, y cada una
// se lanza una sola vez:
//
//   - la que cae en la hora que se salta al adelantar el reloj se lanza en el instante del
//     salto (02:30 en un dia que pasa de 02:00 a 03:00 se lanza a las 03:00); si la
//     expresion tiene tambien una ocurrencia en ese instante, una sola ejecucion cubre ambas;
//   - la que cae en la hora que se repite al atrasarlo se lanza en su primera pasada; en la
//     segunda no se repite, tampoco las de una expresion de cada minuto o cada hora.
//
// @every cuenta tiempo transcurrido y no depende de la zona.
func (c CronSpec) Next(after time.Time) (time.Time, error) {
	if c.schedule == nil {
		return time.Time{}, fmt.Errorf("%w: expression not parsed", ErrInvalidCron)
	}
	if c.every > 0 {
		return c.schedule.Next(after.UTC()), nil
	}
	wall := wallClock(after, c.loc)
	for {
		wall = c.schedule.Next(wall)
		if wall.IsZero() {
			return time.Time{}, invalidCron("the expression never matches a date")
		}
		// Solo vuelve a pasar en la segunda pasada de una hora repetida: esas horas de pared
		// ya se lanzaron en la primera.
		if at := firstInstantAtWall(wall, c.loc); at.After(after) {
			return at, nil
		}
	}
}

// NextAfterDispatch es la ejecucion que sigue a la prevista para scheduled, despachada en now.
//
// Se cuenta desde la hora prevista, no desde now, para que el retraso del ticker no se
// acumule (sin deriva). Si entre scheduled y now quedaron ocurrencias sin lanzar (el proceso
// o la base estuvieron caidos), no se recuperan: la ejecucion que se acaba de despachar las
// cubre y se programa la primera ocurrencia futura, nunca una rafaga de atrasadas.
func (c CronSpec) NextAfterDispatch(scheduled, now time.Time) (time.Time, error) {
	next, err := c.Next(scheduled)
	if err != nil || next.After(now) {
		return next, err
	}
	if c.every > 0 {
		// Primera ocurrencia futura de la misma rejilla (scheduled + k*every), sin iterar.
		missed := now.Sub(next)/c.every + 1
		return next.Add(missed * c.every), nil
	}
	return c.Next(now)
}

// Reconcile es la proxima ejecucion que corresponde a un calendario guardado en stored,
// visto en now. Sirve para los calendarios escritos sin evaluar la expresion.
//
// Un calendario pendiente (stored > now) pasa a la primera ocurrencia futura. Uno vencido
// pasa a la primera ocurrencia en o despues de stored: si esa ya paso, sigue vencido y se
// lanza una vez; si no, es la futura. Para @every la rejilla es la del propio trabajo, asi
// que un vencido se respeta y un pendiente solo se adelanta si esta a mas de un periodo.
// Aplicarlo dos veces da lo mismo que una.
func (c CronSpec) Reconcile(stored, now time.Time) (time.Time, error) {
	if c.every > 0 {
		if stored.After(now.Add(c.every)) {
			return c.Next(now)
		}
		return stored, nil
	}
	if stored.After(now) {
		return c.Next(now)
	}
	return c.Next(stored.Add(-time.Nanosecond))
}
