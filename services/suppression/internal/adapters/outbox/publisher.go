// Package outbox publica los eventos del dominio con garantia transaccional: cada evento
// se encola en platform.event_outbox dentro de la transaccion que cambia la lista, y el
// rele de pkg/outbox (RunForTenants en main) lo entrega a JetStream. Asi ningun alta o
// baja de la lista queda sin su evento aunque el proceso caiga entre el commit y NATS.
package outbox

import (
	"context"

	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/outbox"
	"github.com/alonsosss/corforce-email/services/suppression/internal/domain"
)

// Subjects propios; el stream SUPPRESSION que los cubre se declara en main.
const (
	SubjectEntryAdded   = "suppression.entry.added"
	SubjectEntryRemoved = "suppression.entry.removed"
	source              = "suppression"
)

type Publisher struct {
	q outbox.Execer
}

// NewPublisher recibe el db.ContextPool: Enqueue escribe por la transaccion del contexto.
func NewPublisher(q outbox.Execer) *Publisher { return &Publisher{q: q} }

func (p *Publisher) EntryAdded(ctx context.Context, e *domain.Entry, reasons []domain.Reason) error {
	return p.enqueue(ctx, SubjectEntryAdded, e, reasons)
}

func (p *Publisher) EntryRemoved(ctx context.Context, e *domain.Entry, reasons []domain.Reason) error {
	return p.enqueue(ctx, SubjectEntryRemoved, e, reasons)
}

// enqueue arma el envelope. reason es la causa que entro o se retiro; reasons, las causas
// vigentes que le quedan a la direccion despues (vacio si quedo libre). UserID lleva a
// quien actuo cuando la operacion vino de una persona (borrado manual): es la pista de
// auditoria de una reactivacion.
func (p *Publisher) enqueue(ctx context.Context, subject string, e *domain.Entry, reasons []domain.Reason) error {
	remaining := make([]string, len(reasons))
	for i, r := range reasons {
		remaining[i] = string(r)
	}
	return outbox.Enqueue(ctx, p.q, subject, events.Event{
		Type:     subject,
		Source:   source,
		TenantID: e.TenantID.String(),
		UserID:   middleware.GetUserID(ctx),
		Data: map[string]interface{}{
			"tenant_id": e.TenantID.String(),
			"email":     e.Email,
			"reason":    string(e.Reason),
			"source":    e.Source,
			"reasons":   remaining,
		},
	})
}
