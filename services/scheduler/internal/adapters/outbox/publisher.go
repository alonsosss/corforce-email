// Package outbox emite los eventos del scheduler por la outbox transaccional (pkg/outbox):
// cada evento se encola en la misma transaccion que cambia la ejecucion, y el rele que
// arranca main.go lo entrega despues al stream SCHEDULER de JetStream.
package outbox

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/outbox"
	"github.com/alonsosss/corforce-email/services/scheduler/internal/domain"
	"github.com/google/uuid"
)

const (
	// StreamName y StreamSubjects son lo que main.go declara con EnsureStream: el
	// scheduler es el unico dueno de scheduler.*.
	StreamName     = "SCHEDULER"
	StreamSubjects = "scheduler.>"

	source = "scheduler-service"
)

// Publisher implementa ports.EventPublisher sobre la outbox. Necesita el ContextPool de
// pkg/db para escribir por la transaccion del contexto.
type Publisher struct {
	exec outbox.Execer
}

func NewPublisher(exec outbox.Execer) *Publisher { return &Publisher{exec: exec} }

// JobStarted: una ejecucion queda despachada. Es la orden para el servicio del manejador,
// que la cierra por POST /internal/scheduler/executions/{id}/complete o /fail antes de
// timeout_seconds.
func (p *Publisher) JobStarted(ctx context.Context, job *domain.JobDefinition, exec *domain.JobExecution, timeoutSeconds int) error {
	return p.in(ctx).Publish("scheduler.job.started", events.Event{
		Data: map[string]any{
			"tenant_id":       tenantField(job.TenantID),
			"job_id":          job.ID.String(),
			"execution_id":    exec.ID.String(),
			"handler":         job.Handler,
			"payload":         payloadField(job.Payload),
			"timeout_seconds": timeoutSeconds,
			"attempt":         exec.Attempt(),
		},
	})
}

// JobCompleted: el ejecutor cerro la ejecucion con exito.
func (p *Publisher) JobCompleted(ctx context.Context, job *domain.JobDefinition, exec *domain.JobExecution) error {
	return p.in(ctx).Publish("scheduler.job.completed", events.Event{
		Data: map[string]any{
			"tenant_id":    tenantField(job.TenantID),
			"job_id":       job.ID.String(),
			"execution_id": exec.ID.String(),
			"handler":      job.Handler,
			"attempt":      exec.Attempt(),
			"duration_ms":  exec.Duration,
		},
	})
}

// JobFailed: la ejecucion fallo (lo informo el ejecutor, vencio su plazo o su manejador ya
// no esta permitido). retry_execution_id y retry_at dicen si habra otro intento.
func (p *Publisher) JobFailed(ctx context.Context, job *domain.JobDefinition, exec, retry *domain.JobExecution) error {
	return p.in(ctx).Publish("scheduler.job.failed", events.Event{
		Data: map[string]any{
			"tenant_id":          tenantField(job.TenantID),
			"job_id":             job.ID.String(),
			"execution_id":       exec.ID.String(),
			"handler":            job.Handler,
			"attempt":            exec.Attempt(),
			"reason":             stringField(exec.FailureReason),
			"error":              stringField(exec.ErrorMessage),
			"retry_execution_id": retryID(retry),
			"retry_at":           retryAt(retry),
		},
	})
}

// in liga el publicador a la transaccion del contexto. La forma Publish(subject, evento)
// es la que leen los registros de contratos (ops/scaffold/eventcontracts y gen-events).
func (p *Publisher) in(ctx context.Context) transactional {
	return transactional{ctx: ctx, exec: p.exec}
}

type transactional struct {
	ctx  context.Context
	exec outbox.Execer
}

// Publish completa el sobre y encola. La empresa del sobre es la de la base en la que vive
// la ejecucion, tambien para un trabajo de plataforma (cuyo tenant_id en data es null): es
// la que el ejecutor devuelve en X-Tenant-ID al cerrarla.
func (t transactional) Publish(subject string, evt events.Event) error {
	evt.Type = subject
	evt.Source = source
	evt.TenantID = middleware.GetTenantID(t.ctx)
	evt.UserID = middleware.GetUserID(t.ctx)
	return outbox.Enqueue(t.ctx, t.exec, subject, evt)
}

func tenantField(id *uuid.UUID) any {
	if id == nil {
		return nil
	}
	return id.String()
}

// payloadField entrega el payload como JSON y no como texto: viene de una columna jsonb.
func payloadField(p *string) any {
	if p == nil {
		return nil
	}
	return rawJSON(*p)
}

type rawJSON string

func (r rawJSON) MarshalJSON() ([]byte, error) { return []byte(r), nil }

func stringField(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func retryID(retry *domain.JobExecution) any {
	if retry == nil {
		return nil
	}
	return retry.ID.String()
}

func retryAt(retry *domain.JobExecution) any {
	if retry == nil || retry.NextAttemptAt == nil {
		return nil
	}
	return retry.NextAttemptAt.UTC().Format(time.RFC3339Nano)
}
