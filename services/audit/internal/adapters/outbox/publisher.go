// Package outbox publica el anuncio de cada ancla de cadena con garantia transaccional: el evento
// se encola en platform.event_outbox dentro de la transaccion que guarda el ancla, y el rele de
// pkg/outbox (RunForTenants en main) lo entrega a JetStream. Un ancla anunciada existe, y una
// guardada se anuncia aunque el proceso caiga entre el commit y NATS.
package outbox

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/pkg/outbox"
	"github.com/alonsosss/corforce-email/services/audit/internal/domain"
)

// SubjectChainAnchored es propio de audit; el stream AUDIT_CHAIN que lo cubre se declara en main.
const (
	SubjectChainAnchored = "audit.chain.anchored"
	source               = "audit-service"
)

type Publisher struct {
	q outbox.Execer
}

// NewPublisher recibe el db.ContextPool: Enqueue escribe por la transaccion del contexto.
func NewPublisher(q outbox.Execer) *Publisher { return &Publisher{q: q} }

// ChainAnchored lleva la cabeza anclada: es lo que un sistema fuera de la base guarda para
// comparar despues. No lleva contenido de ninguna fila, solo su posicion y su hash.
func (p *Publisher) ChainAnchored(ctx context.Context, a *domain.ChainAnchor) error {
	return outbox.Enqueue(ctx, p.q, SubjectChainAnchored, events.Event{
		Type:     SubjectChainAnchored,
		Source:   source,
		TenantID: a.TenantID.String(),
		Data: map[string]interface{}{
			"tenant_id":    a.TenantID.String(),
			"chain":        string(a.Chain),
			"head_seq":     a.HeadSeq,
			"head_hash":    a.HeadHash,
			"hash_version": a.HashVersion,
			"anchored_at":  a.AnchoredAt.UTC().Format(time.RFC3339Nano),
		},
	})
}
