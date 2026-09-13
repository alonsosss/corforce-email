// Package outbox emite los eventos del dominio por la outbox transaccional (pkg/outbox):
// el evento se encola en la misma transaccion que publica la version, y el rele que
// arranca main.go lo entrega despues al stream TEMPLATES de JetStream.
package outbox

import (
	"context"

	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/outbox"
	"github.com/google/uuid"
)

const (
	// StreamName y StreamSubjects son lo que main.go declara con EnsureStream.
	StreamName     = "TEMPLATES"
	StreamSubjects = "templates.>"

	source = "templates-service"
)

// Publisher implementa ports.EventPublisher sobre la outbox. Necesita el ContextPool de
// pkg/db para escribir por la transaccion del contexto.
type Publisher struct {
	exec outbox.Execer
}

func NewPublisher(exec outbox.Execer) *Publisher { return &Publisher{exec: exec} }

// TemplatePublished: una version pasa a ser la publicada de su plantilla.
func (p *Publisher) TemplatePublished(ctx context.Context, tenantID, templateID uuid.UUID, version int) error {
	return p.in(ctx).Publish("templates.template.published", events.Event{
		Data: map[string]any{
			"tenant_id":   tenantID.String(),
			"template_id": templateID.String(),
			"version":     version,
		},
	})
}

// in liga el publicador a la transaccion del contexto. La forma Publish(subject, evento)
// es la que leen los registros de contratos (ops/scaffold/eventcontracts y gen-events):
// asi el payload queda documentado y verificado frente a sus consumidores.
func (p *Publisher) in(ctx context.Context) transactional {
	return transactional{ctx: ctx, exec: p.exec}
}

// transactional vive solo durante la llamada que lo crea; el contexto que guarda es el de
// esa transaccion.
type transactional struct {
	ctx  context.Context
	exec outbox.Execer
}

// Publish completa el sobre (fuente, empresa y usuario del contexto) y encola.
func (t transactional) Publish(subject string, evt events.Event) error {
	evt.Type = subject
	evt.Source = source
	evt.TenantID = middleware.GetTenantID(t.ctx)
	evt.UserID = middleware.GetUserID(t.ctx)
	if data, ok := evt.Data.(map[string]any); ok {
		if tid, ok := data["tenant_id"].(string); ok && tid != "" {
			evt.TenantID = tid
		}
	}
	return outbox.Enqueue(t.ctx, t.exec, subject, evt)
}
