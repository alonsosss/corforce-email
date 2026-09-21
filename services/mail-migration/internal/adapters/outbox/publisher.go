// Package outbox encola los eventos de auditoria de las migraciones en platform.event_outbox de la
// base de la empresa, dentro de la transaccion que cambia el trabajo; el rele de pkg/outbox
// (RunForTenants en main) los entrega al stream MIGRATION. Ningun payload lleva la contrasena de
// origen ni el usuario de la cuenta de origen: solo el servidor, que es lo que interesa auditar.
package outbox

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/pkg/outbox"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/domain"
	"github.com/google/uuid"
)

const (
	SubjectCreated         = "migration.job.created"
	SubjectStarted         = "migration.job.started"
	SubjectCancelRequested = "migration.job.cancel_requested"
	SubjectCompleted       = "migration.job.completed"
	SubjectFailed          = "migration.job.failed"
	SubjectCancelled       = "migration.job.cancelled"

	// Stream y patron del stream que declara main; el consumidor de auditoria lee migration.>.
	Stream  = "MIGRATION"
	Pattern = "migration.>"

	source = "mail-migration"
)

type Publisher struct {
	q outbox.Execer
}

// NewPublisher recibe el db.ContextPool: Enqueue escribe por la transaccion del contexto.
func NewPublisher(q outbox.Execer) *Publisher { return &Publisher{q: q} }

func (p *Publisher) Created(ctx context.Context, j *domain.Job) error {
	data := base(j)
	data["mailbox_username"] = j.MailboxUsername
	data["source_host"] = j.SourceHost
	data["source_port"] = j.SourcePort
	data["source_tls"] = string(j.SourceTLS)
	return p.enqueue(ctx, SubjectCreated, j, data, j.RequestedBy)
}

func (p *Publisher) Started(ctx context.Context, j *domain.Job) error {
	data := base(j)
	data["attempt"] = j.Attempt
	return p.enqueue(ctx, SubjectStarted, j, data, uuid.Nil)
}

func (p *Publisher) CancelRequested(ctx context.Context, j *domain.Job, actorID uuid.UUID) error {
	return p.enqueue(ctx, SubjectCancelRequested, j, base(j), actorID)
}

// Finished publica el estado final: completed, failed o cancelled. actorID es quien cancelo, o nulo
// si lo cerro el ejecutor o el servicio.
func (p *Publisher) Finished(ctx context.Context, j *domain.Job, actorID uuid.UUID) error {
	data := base(j)
	data["messages_copied"] = j.Progress.MessagesCopied
	data["messages_skipped"] = j.Progress.MessagesSkipped
	data["messages_failed"] = j.Progress.MessagesFailed
	data["bytes_copied"] = j.Progress.BytesCopied
	data["folders_done"] = j.Progress.FoldersDone
	if j.LastError != nil {
		data["error_code"] = string(j.LastError.Code)
	}
	switch j.Status {
	case domain.StatusFailed:
		return p.enqueue(ctx, SubjectFailed, j, data, actorID)
	case domain.StatusCancelled:
		return p.enqueue(ctx, SubjectCancelled, j, data, actorID)
	default:
		return p.enqueue(ctx, SubjectCompleted, j, data, actorID)
	}
}

func base(j *domain.Job) map[string]interface{} {
	return map[string]interface{}{
		"tenant_id":  j.TenantID.String(),
		"job_id":     j.ID.String(),
		"mailbox_id": j.MailboxID.String(),
		"status":     string(j.Status),
	}
}

// enqueue arma el envelope. UserID lleva a quien pidio la accion: es la pista de auditoria.
func (p *Publisher) enqueue(ctx context.Context, subject string, j *domain.Job, data map[string]interface{}, actorID uuid.UUID) error {
	evt := events.Event{Type: subject, Source: source, TenantID: j.TenantID.String(), Data: data, Timestamp: time.Now().UTC()}
	if actorID != uuid.Nil {
		evt.UserID = actorID.String()
	}
	return outbox.Enqueue(ctx, p.q, subject, evt)
}
