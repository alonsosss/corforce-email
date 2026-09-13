package domain

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// RunStatus: waiting (espera su next_run_at), running (un trabajador tiene su reserva) y
// los terminales completed, failed, cancelled y skipped.
type RunStatus string

const (
	RunWaiting   RunStatus = "waiting"
	RunRunning   RunStatus = "running"
	RunCompleted RunStatus = "completed"
	RunFailed    RunStatus = "failed"
	RunCancelled RunStatus = "cancelled"
	RunSkipped   RunStatus = "skipped"
)

func RunStatuses() []RunStatus {
	return []RunStatus{RunWaiting, RunRunning, RunCompleted, RunFailed, RunCancelled, RunSkipped}
}

func ParseRunStatus(s string) (RunStatus, bool) {
	for _, st := range RunStatuses() {
		if string(st) == s {
			return st, true
		}
	}
	return "", false
}

// EntryOnce es la clave de entrada de un flujo sin reentrada: con la restriccion UNIQUE
// (workflow, contact, entry_key) un contacto entra una sola vez.
const EntryOnce = "once"

const (
	// MaxStepAttempts: fallos transitorios seguidos de un paso antes de dar la ejecucion
	// por fallida.
	MaxStepAttempts = 10
	// RunLease es cuanto dura la reserva de un paso. Debe superar con holgura lo que tarda
	// un paso (dos llamadas de CallTimeout): si el trabajador muere, al vencer la reserva
	// otro retoma el paso con la misma clave de idempotencia.
	RunLease = 5 * time.Minute
	// CallTimeout acota cada llamada a un vecino dentro de un paso.
	CallTimeout = 30 * time.Second
	// defaultRetryAfter se aplica a un 429 sin Retry-After legible.
	defaultRetryAfter = time.Minute
	maxRetryAfter     = time.Hour
	minRetryAfter     = 5 * time.Second
	baseBackoff       = 30 * time.Second
	maxBackoff        = 30 * time.Minute
)

// Codigos con los que termina o se aplaza un paso. Los de los vecinos (SENDING_RESTRICTED,
// TEMPLATE_NOT_MARKETING...) se guardan tal cual los devuelven.
const (
	CodeContactNotSendable = "CONTACT_NOT_SENDABLE"
	CodeRateLimited        = "RATE_LIMITED"
	CodeUnavailable        = "UNAVAILABLE"
	CodeWorkflowArchived   = "WORKFLOW_ARCHIVED"
	CodeSendingRestricted  = "SENDING_RESTRICTED"
	CodePlanLimitReached   = "PLAN_LIMIT_REACHED"
)

// BlockingCodes son los 403 de transactional que cuentan para pausar un flujo: la empresa
// no puede enviar y seguir intentandolo solo quema contactos.
func BlockingCodes() []string { return []string{CodeSendingRestricted, CodePlanLimitReached} }

type Run struct {
	ID             uuid.UUID  `json:"id"`
	TenantID       uuid.UUID  `json:"tenant_id"`
	WorkflowID     uuid.UUID  `json:"workflow_id"`
	ContactID      uuid.UUID  `json:"contact_id"`
	TriggerEventID string     `json:"trigger_event_id"`
	EntryKey       string     `json:"-"`
	StepIndex      int        `json:"step_index"`
	Status         RunStatus  `json:"status"`
	NextRunAt      time.Time  `json:"next_run_at"`
	Attempts       int        `json:"attempts"`
	ErrorCode      string     `json:"error_code"`
	LastError      string     `json:"last_error"`
	LeaseToken     *uuid.UUID `json:"-"`
	LeaseUntil     *time.Time `json:"-"`
	FinishedAt     *time.Time `json:"finished_at"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// NewRun es la entrada de un contacto en un flujo: primer paso, debido ya.
func NewRun(w *Workflow, ev TriggerEvent, now time.Time) *Run {
	key := EntryOnce
	if w.ReEntry {
		key = ev.EventID
	}
	return &Run{
		ID: uuid.New(), TenantID: w.TenantID, WorkflowID: w.ID, ContactID: ev.ContactID,
		TriggerEventID: ev.EventID, EntryKey: key, Status: RunWaiting, NextRunAt: now,
	}
}

// IdempotencyKey identifica el envio de un paso. Depende solo de la ejecucion y del paso:
// un reintento tras una caida lleva la misma clave y transactional devuelve lo ya creado.
func (r *Run) IdempotencyKey() string {
	return fmt.Sprintf("automation:%s:step:%d", r.ID, r.StepIndex)
}

// RetryBackoff es la espera tras el fallo transitorio numero attempts (desde 1).
func RetryBackoff(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	d := baseBackoff
	for i := 1; i < attempts && d < maxBackoff; i++ {
		d *= 2
	}
	if d > maxBackoff {
		d = maxBackoff
	}
	return d
}

// ClampRetryAfter acota la espera que pide un 429.
func ClampRetryAfter(d time.Duration) time.Duration {
	switch {
	case d <= 0:
		return defaultRetryAfter
	case d < minRetryAfter:
		return minRetryAfter
	case d > maxRetryAfter:
		return maxRetryAfter
	}
	return d
}
