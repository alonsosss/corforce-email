package domain

import "time"

// ResumeAt es la proxima ejecucion de un trabajo inactivo que se reactiva en now. schedule
// es su fila de calendario, o nil si no tiene. Ninguna regla lanza lo que se dejo de lanzar
// mientras estuvo parado ni deja next_run_at en el pasado:
//
//   - cron: la primera ocurrencia de su expresion posterior a now, en su zona, como al
//     crearlo.
//   - interval: sigue su rejilla (la hora guardada mas k periodos), sin deriva: si la hora
//     guardada aun no llego se respeta; si ya paso, la primera de la rejilla posterior a
//     now. Una hora guardada a mas de un periodo de now (el intervalo se acorto mientras
//     estaba parado) o la falta de calendario cuentan un periodo desde now.
//   - one_time: si el calendario nunca lo despacho, now (sale en la pasada siguiente, como
//     al crearlo); si ya lo despacho (OneTimeAlreadyRun), ErrOneTimeAlreadyRun.
func (j *JobDefinition) ResumeAt(schedule *JobSchedule, now time.Time) (time.Time, error) {
	switch j.JobType {
	case JobTypeCron:
		return j.FirstRunAt(now)
	case JobTypeInterval:
		period, err := j.intervalPeriod()
		if err != nil {
			return time.Time{}, err
		}
		var stored time.Time
		if schedule != nil {
			stored = schedule.NextRunAt
		}
		return nextOnGrid(stored, now, period), nil
	case JobTypeOneTime:
		var lastRunAt *time.Time
		if schedule != nil {
			lastRunAt = schedule.LastRunAt
		}
		if j.OneTimeAlreadyRun(lastRunAt) {
			return time.Time{}, ErrOneTimeAlreadyRun
		}
		return now, nil
	}
	return time.Time{}, j.unschedulableType()
}

// nextOnGrid es la primera hora de la rejilla anchor + k*period estrictamente posterior a
// now, sin iterar. Sin anchor, o con uno a mas de un periodo de now, es now + period.
func nextOnGrid(anchor, now time.Time, period time.Duration) time.Time {
	first := now.Add(period)
	if anchor.IsZero() || anchor.After(first) {
		return first
	}
	if anchor.After(now) {
		return anchor
	}
	next := anchor.Add((now.Sub(anchor)/period + 1) * period)
	// Sub satura a unos 292 anos: una hora guardada tan antigua no es una rejilla.
	if !next.After(now) {
		return first
	}
	return next
}
