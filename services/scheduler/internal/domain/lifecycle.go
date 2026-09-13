package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// MaxErrorMessageRunes acota el motivo que guarda una ejecucion fallida: un ejecutor puede
// mandar una traza entera y la columna no es un almacen de logs.
const MaxErrorMessageRunes = 4000

// nulEscape es el escape JSON del caracter NUL (barra invertida, u y cuatro ceros): JSON
// lo admite y jsonb lo rechaza, asi que se filtra antes de llegar a la base.
var nulEscape = []byte{'\\', 'u', '0', '0', '0', '0'}

// Validate comprueba lo que el ciclo de vida da por hecho de una definicion.
func (j *JobDefinition) Validate() error {
	if _, err := LoadTimezone(j.Timezone); err != nil {
		return err
	}
	switch j.JobType {
	case JobTypeCron:
		if _, err := ParseCron(j.CronExpr(), j.Timezone); err != nil {
			return err
		}
	case JobTypeOneTime:
	case JobTypeInterval:
		if j.IntervalMinutes == nil || *j.IntervalMinutes <= 0 {
			return fmt.Errorf("%w: interval_minutes must be positive for interval jobs", ErrInvalidJob)
		}
	default:
		return fmt.Errorf("%w: job_type must be %s, %s or %s", ErrInvalidJob, JobTypeCron, JobTypeInterval, JobTypeOneTime)
	}
	if j.MaxRetries < 0 {
		return fmt.Errorf("%w: max_retries must not be negative", ErrInvalidJob)
	}
	if j.TimeoutSeconds < 0 {
		return fmt.Errorf("%w: timeout_seconds must not be negative", ErrInvalidJob)
	}
	if j.Payload != nil {
		if err := validJSONDocument([]byte(*j.Payload)); err != nil {
			return fmt.Errorf("%w: payload %s", ErrInvalidJob, err.Error())
		}
	}
	return nil
}

// NormalizeResult prepara el resultado que informa un ejecutor para la columna jsonb: nil
// si no hay resultado, error si no se podria guardar.
func NormalizeResult(raw json.RawMessage) (*string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	if err := validJSONDocument(trimmed); err != nil {
		return nil, fmt.Errorf("%w: result %s", ErrInvalidReport, err.Error())
	}
	s := string(trimmed)
	return &s, nil
}

func validJSONDocument(b []byte) error {
	if !json.Valid(b) {
		return errors.New("must be a valid JSON document")
	}
	if bytes.Contains(b, nulEscape) {
		return errors.New("must not contain the NUL character")
	}
	return nil
}

// IsActive indica si la ejecucion aun puede cerrarse.
func (e *JobExecution) IsActive() bool {
	return e.Status == StatusPending || e.Status == StatusRunning
}

// Attempt es el numero de intento, empezando en 1.
func (e *JobExecution) Attempt() int { return e.RetryCount + 1 }

// Dispatch pasa la ejecucion a running: desde ahora corre el plazo del manejador.
func (e *JobExecution) Dispatch(now time.Time, timeoutSeconds int) {
	started := now
	deadline := now.Add(time.Duration(timeoutSeconds) * time.Second)
	e.Status = StatusRunning
	e.StartedAt = &started
	e.DeadlineAt = &deadline
	e.NextAttemptAt = nil
}

// Complete cierra la ejecucion con exito. Devuelve false, sin error, si ya estaba completada
// o cancelada: repetir el cierre no cambia nada. Completar una fallida es un conflicto.
func (e *JobExecution) Complete(now time.Time, result *string) (bool, error) {
	switch e.Status {
	case StatusPending, StatusRunning:
		e.Status = StatusCompleted
		e.Result = result
		e.close(now)
		return true, nil
	case StatusCompleted, StatusCancelled:
		return false, nil
	default:
		return false, ErrExecutionConflict
	}
}

// Fail cierra la ejecucion como fallida con su motivo. Devuelve false, sin error, si ya
// estaba fallida o cancelada. Fallar una completada es un conflicto.
func (e *JobExecution) Fail(now time.Time, reason, message string) (bool, error) {
	switch e.Status {
	case StatusPending, StatusRunning:
		msg := sanitizeMessage(message)
		e.Status = StatusFailed
		e.FailureReason = &reason
		e.ErrorMessage = &msg
		e.close(now)
		return true, nil
	case StatusFailed, StatusCancelled:
		return false, nil
	default:
		return false, ErrExecutionConflict
	}
}

// Cancel detiene una ejecucion activa. Cancelar dos veces no cambia nada; cancelar una que
// ya termino es un error.
func (e *JobExecution) Cancel(now time.Time) (bool, error) {
	switch e.Status {
	case StatusPending, StatusRunning:
		e.Status = StatusCancelled
		e.close(now)
		return true, nil
	case StatusCancelled:
		return false, nil
	default:
		return false, ErrExecutionClosed
	}
}

func (e *JobExecution) close(now time.Time) {
	closed := now
	e.CompletedAt = &closed
	e.NextAttemptAt = nil
	if e.StartedAt != nil {
		d := now.Sub(*e.StartedAt).Milliseconds()
		if d < 0 {
			d = 0
		}
		e.Duration = &d
	}
}

// NextAttempt crea el reintento de una ejecucion fallida, en pending hasta su hora.
func (e *JobExecution) NextAttempt(id uuid.UUID, now time.Time, delay time.Duration) *JobExecution {
	at := now.Add(delay)
	prev := e.ID
	return &JobExecution{
		ID:            id,
		JobID:         e.JobID,
		TenantID:      e.TenantID,
		Status:        StatusPending,
		RetryCount:    e.RetryCount + 1,
		RetryOf:       &prev,
		NextAttemptAt: &at,
		CreatedAt:     now,
	}
}

// HasRetriesLeft indica si la ejecucion aun admite un reintento dentro de max_retries.
func (j *JobDefinition) HasRetriesLeft(e *JobExecution) bool {
	return e.RetryCount < j.MaxRetries
}

// sanitizeMessage deja el motivo en texto que Postgres acepta (UTF-8 valido y sin NUL) y
// dentro de MaxErrorMessageRunes.
func sanitizeMessage(s string) string {
	s = strings.ToValidUTF8(s, string(utf8.RuneError))
	s = strings.ReplaceAll(s, string(rune(0)), "")
	if utf8.RuneCountInString(s) <= MaxErrorMessageRunes {
		return s
	}
	return string([]rune(s)[:MaxErrorMessageRunes])
}

// RetryPolicy es la espera creciente entre reintentos automaticos.
type RetryPolicy struct {
	BaseDelay time.Duration
	MaxDelay  time.Duration
}

func (p RetryPolicy) Validate() error {
	if p.BaseDelay <= 0 {
		return errors.New("la espera base de reintento debe ser positiva")
	}
	if p.MaxDelay < p.BaseDelay {
		return errors.New("la espera maxima de reintento no puede ser menor que la base")
	}
	return nil
}

// Delay es la espera antes del reintento numero n (1 es el primero): BaseDelay * 2^(n-1),
// con techo MaxDelay.
func (p RetryPolicy) Delay(n int) time.Duration {
	d := p.BaseDelay
	for i := 1; i < n; i++ {
		if d >= p.MaxDelay/2 {
			return p.MaxDelay
		}
		d *= 2
	}
	if d > p.MaxDelay {
		return p.MaxDelay
	}
	return d
}
