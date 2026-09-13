package domain

import "time"

// FirstRunAt es la primera ejecucion de una definicion contada desde now: la del alta y la
// de una edicion que cambia el calendario (ver ScheduleChanged). Nunca queda en el pasado
// ni arrastra nada de la definicion anterior, asi que una edicion no lanza atrasadas:
//
//   - cron: la primera ocurrencia de su expresion posterior a now, en su zona.
//   - interval: now mas un periodo; su rejilla empieza ahi.
//   - one_time: now, que sale en la pasada siguiente del planificador.
func (j *JobDefinition) FirstRunAt(now time.Time) (time.Time, error) {
	switch j.JobType {
	case JobTypeCron:
		spec, err := ParseCron(j.CronExpr(), j.Timezone)
		if err != nil {
			return time.Time{}, err
		}
		return spec.Next(now)
	case JobTypeInterval:
		period, err := j.intervalPeriod()
		if err != nil {
			return time.Time{}, err
		}
		return now.Add(period), nil
	case JobTypeOneTime:
		return now, nil
	}
	return time.Time{}, j.unschedulableType()
}

// NextAfterDispatch es la ejecucion que sigue a la prevista para scheduled, que el
// calendario acaba de despachar en now. Se cuenta desde scheduled y no desde now, para que
// el retraso del ticker no se acumule (sin deriva). Las ocurrencias que quedaron entre
// scheduled y now (el proceso o la base estuvieron caidos) no se recuperan: la ejecucion
// despachada las cubre y se programa la primera futura, nunca una rafaga.
//
//   - cron: ver CronSpec.NextAfterDispatch.
//   - interval: la primera hora de su rejilla (scheduled mas k periodos, k >= 1) posterior
//     a now. Cuenta tiempo transcurrido: ni la zona ni los cambios de hora la mueven.
//   - one_time: now; el trabajo se desactiva en la misma transaccion.
func (j *JobDefinition) NextAfterDispatch(scheduled, now time.Time) (time.Time, error) {
	switch j.JobType {
	case JobTypeCron:
		spec, err := ParseCron(j.CronExpr(), j.Timezone)
		if err != nil {
			return time.Time{}, err
		}
		return spec.NextAfterDispatch(scheduled, now)
	case JobTypeInterval:
		period, err := j.intervalPeriod()
		if err != nil {
			return time.Time{}, err
		}
		// Solo se despacha lo vencido; una hora prevista posterior a now no se repite.
		if now.Before(scheduled) {
			now = scheduled
		}
		return nextOnGrid(scheduled, now, period), nil
	case JobTypeOneTime:
		return now, nil
	}
	return time.Time{}, j.unschedulableType()
}

// ScheduleChanged indica si la definicion cambia cuando corre el trabajo respecto de prev,
// la guardada: el tipo y, dentro del tipo, lo que decide su calendario (la expresion y la
// zona de un cron, los minutos de un interval). El nombre, el manejador, el payload, los
// reintentos o el plazo no lo cambian.
func (j *JobDefinition) ScheduleChanged(prev *JobDefinition) bool {
	if prev == nil || prev.JobType != j.JobType {
		return true
	}
	switch j.JobType {
	case JobTypeCron:
		return prev.CronExpr() != j.CronExpr() || prev.Timezone != j.Timezone
	case JobTypeInterval:
		return !sameMinutes(prev.IntervalMinutes, j.IntervalMinutes)
	}
	return false
}

// OneTimeAlreadyRun indica un one_time que el calendario ya despacho. lastRunAt es la
// ultima pasada del calendario (last_run_at); lanzarlo a mano no la cambia. Es la regla con
// la que ResumeAt lo rechaza (ErrOneTimeAlreadyRun) y la que el API publica para que la UI
// no ofrezca reactivarlo.
func (j *JobDefinition) OneTimeAlreadyRun(lastRunAt *time.Time) bool {
	return j.JobType == JobTypeOneTime && lastRunAt != nil
}

// intervalPeriod es el periodo de un interval con los minutos dentro de su rango. Unos
// minutos guardados sin validar (nulos, cero o negativos) lanzarian el trabajo en cada
// pasada.
func (j *JobDefinition) intervalPeriod() (time.Duration, error) {
	if j.IntervalMinutes == nil {
		return 0, invalidField(FieldIntervalMinutes, RuleRequired, ErrInvalidJob, "interval_minutes is required for interval jobs")
	}
	minutes := *j.IntervalMinutes
	if minutes < MinIntervalMinutes || minutes > MaxIntervalMinutes {
		return 0, invalidField(FieldIntervalMinutes, RuleOutOfRange, ErrInvalidJob, "interval_minutes must be between %d and %d", MinIntervalMinutes, MaxIntervalMinutes)
	}
	return time.Duration(minutes) * time.Minute, nil
}

func (j *JobDefinition) unschedulableType() error {
	return invalidField(FieldJobType, RuleNotAllowed, ErrInvalidJob, "job_type %q cannot be scheduled", j.JobType)
}

func sameMinutes(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
