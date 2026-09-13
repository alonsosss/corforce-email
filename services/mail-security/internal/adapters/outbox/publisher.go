// Package outbox emite los eventos de la cuarentena por la outbox de la celda
// (platform.event_outbox, pkg/outbox): cada evento se encola en la misma transaccion que
// la fila que lo origina, y el rele de la celda (main.go) lo entrega despues al stream
// MAIL_SECURITY. Subjects, sobre y payloads son los que se publicaban directamente.
package outbox

import (
	"context"

	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/pkg/outbox"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
)

const source = "mail-security-service"

// Publisher implementa ports.EventPublisher. Recibe el db.ContextPool: Enqueue escribe
// por la transaccion del contexto, que es la del caso de uso.
type Publisher struct {
	exec outbox.Execer
}

func NewPublisher(exec outbox.Execer) *Publisher { return &Publisher{exec: exec} }

// Cada payload se escribe en su llamada, con la forma Publish(subject, evento) que lee
// ops/scaffold/eventcontracts.

func (p *Publisher) QuarantineStored(ctx context.Context, item *domain.QuarantineItem) error {
	return p.in(ctx).Publish(domain.SubjectQuarantineStored, events.Event{TenantID: item.TenantID.String(), Data: map[string]interface{}{
		"id": item.ID.String(), "tenant_id": item.TenantID.String(), "rcpt": item.Rcpt,
		"sender": item.Sender, "subject": item.Subject, "score": item.Score.String(), "qid": item.QID,
	}})
}

func (p *Publisher) QuarantineReleased(ctx context.Context, item *domain.QuarantineItem, userID string) error {
	return p.in(ctx).Publish(domain.SubjectQuarantineReleased, events.Event{TenantID: item.TenantID.String(), Data: map[string]interface{}{
		"id": item.ID.String(), "tenant_id": item.TenantID.String(), "rcpt": item.Rcpt, "user_id": userID,
	}})
}

func (p *Publisher) in(ctx context.Context) transactional {
	return transactional{ctx: ctx, exec: p.exec}
}

// transactional vive solo durante la llamada que lo crea; su contexto es el de la
// transaccion del caso de uso.
type transactional struct {
	ctx  context.Context
	exec outbox.Execer
}

// Publish completa el sobre (tipo y fuente; la empresa la fija el llamante) y encola. El
// id lo asigna Enqueue y el rele lo conserva en cada reintento.
func (t transactional) Publish(subject string, evt events.Event) error {
	evt.Type = subject
	evt.Source = source
	return outbox.Enqueue(t.ctx, t.exec, subject, evt)
}
