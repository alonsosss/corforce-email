// Package outbox publica los cambios de estado de reputacion con garantia transaccional:
// el evento se encola en platform.event_outbox dentro de la transaccion que cambia el
// estado, y el rele de pkg/outbox (RunForTenants en main) lo entrega a JetStream.
package outbox

import (
	"context"

	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/pkg/outbox"
	"github.com/alonsosss/corforce-email/services/reputation/internal/domain"
)

// Subject propio; el stream REPUTATION que lo cubre se declara en main.
const (
	SubjectStateChanged = "reputation.tenant.state_changed"
	source              = "reputation"
)

type Publisher struct {
	q outbox.Execer
}

// NewPublisher recibe el db.ContextPool: Enqueue escribe por la transaccion del contexto.
func NewPublisher(q outbox.Execer) *Publisher { return &Publisher{q: q} }

// StateChanged encola la transicion. Las tasas van como texto decimal, igual que en el
// API. UserID lleva al superadmin cuando el cambio fue manual.
func (p *Publisher) StateChanged(ctx context.Context, c domain.Change) error {
	userID := ""
	if c.ChangedBy != nil {
		userID = c.ChangedBy.String()
	}
	return outbox.Enqueue(ctx, p.q, SubjectStateChanged, events.Event{
		Type:     SubjectStateChanged,
		Source:   source,
		TenantID: c.TenantID.String(),
		UserID:   userID,
		Data: map[string]interface{}{
			"tenant_id":      c.TenantID.String(),
			"class":          string(c.Class),
			"from":           string(c.From),
			"to":             string(c.To),
			"bounce_rate":    c.BounceRate.StringFixed(domain.RateScale),
			"complaint_rate": c.ComplaintRate.StringFixed(domain.RateScale),
			"reason":         c.Reason,
			"manual":         c.Manual,
		},
	})
}
