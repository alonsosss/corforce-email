package domain

import (
	"fmt"
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

// CronSpec es una expresion cron ya validada. Se evalua en UTC: el modelo de datos no tiene
// zona horaria por trabajo.
type CronSpec struct {
	schedule cron.Schedule
	// every es el periodo de @every; cero para las expresiones de calendario.
	every time.Duration
}

// ParseCron valida una expresion: cinco campos o uno de los descriptores admitidos.
func ParseCron(expr string) (CronSpec, error) {
	if len(expr) > MaxCronExpressionLength {
		return CronSpec{}, fmt.Errorf("%w: longer than %d characters", ErrInvalidCron, MaxCronExpressionLength)
	}
	spec := strings.TrimSpace(expr)
	if spec == "" {
		return CronSpec{}, fmt.Errorf("%w: cron_expression is required for cron jobs", ErrInvalidCron)
	}
	// La libreria aceptaria un prefijo TZ=/CRON_TZ= y cargaria la zona del sistema: la zona
	// no es parte de la expresion, es un dato del trabajo que todavia no existe.
	if strings.HasPrefix(spec, "TZ=") || strings.HasPrefix(spec, "CRON_TZ=") {
		return CronSpec{}, fmt.Errorf("%w: time zones are not supported, expressions run in UTC", ErrInvalidCron)
	}
	if strings.HasPrefix(spec, "@") && !cronDescriptors[spec] && !strings.HasPrefix(spec, everyPrefix) {
		return CronSpec{}, fmt.Errorf("%w: descriptor must be @hourly, @daily, @weekly, @monthly or @every <duration>", ErrInvalidCron)
	}
	schedule, err := cronParser.Parse(spec)
	if err != nil {
		return CronSpec{}, fmt.Errorf("%w: %s", ErrInvalidCron, err.Error())
	}
	if every, ok := schedule.(cron.ConstantDelaySchedule); ok {
		// La libreria sube a un segundo cualquier periodo menor, cero o negativo incluidos.
		if every.Delay < MinCronEvery {
			return CronSpec{}, fmt.Errorf("%w: @every must be at least %s", ErrInvalidCron, MinCronEvery)
		}
		return CronSpec{schedule: schedule, every: every.Delay}, nil
	}
	if schedule.Next(neverProbe).IsZero() {
		return CronSpec{}, fmt.Errorf("%w: the expression never matches a date", ErrInvalidCron)
	}
	return CronSpec{schedule: schedule}, nil
}

// NextRun es la primera ocurrencia de expr estrictamente posterior a after, en UTC.
func NextRun(expr string, after time.Time) (time.Time, error) {
	spec, err := ParseCron(expr)
	if err != nil {
		return time.Time{}, err
	}
	return spec.Next(after)
}

// Next es la primera ocurrencia estrictamente posterior a after, en UTC.
func (c CronSpec) Next(after time.Time) (time.Time, error) {
	if c.schedule == nil {
		return time.Time{}, fmt.Errorf("%w: expression not parsed", ErrInvalidCron)
	}
	next := c.schedule.Next(after.UTC())
	if next.IsZero() {
		return time.Time{}, fmt.Errorf("%w: the expression never matches a date", ErrInvalidCron)
	}
	return next, nil
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
