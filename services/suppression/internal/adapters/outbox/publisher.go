// Package outbox publica los eventos del dominio con garantia transaccional: cada evento
// se encola en platform.event_outbox dentro de la transaccion que cambia la lista, y el
// rele de pkg/outbox (RunForTenants en main) lo entrega a JetStream. Asi ningun alta,
// baja o caducidad de la lista queda sin su evento aunque el proceso caiga entre el commit
// y NATS.
package outbox

import (
	"context"
	"errors"
	"time"

	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/outbox"
	"github.com/alonsosss/corforce-email/services/suppression/internal/domain"
	"github.com/google/uuid"
)

// Subjects propios; el stream SUPPRESSION que los cubre se declara en main.
const (
	SubjectEntryAdded   = "suppression.entry.added"
	SubjectEntryRemoved = "suppression.entry.removed"
	SubjectEntryExpired = "suppression.entry.expired"
	source              = "suppression"
)

// errExpiryWithoutDate: una caducidad sin la fecha que caduco no se puede distinguir de la
// de una renovacion posterior de la misma causa. La transaccion que la anuncia se revierte.
var errExpiryWithoutDate = errors.New("suppression: caducidad anunciada sin expires_at")

type Publisher struct {
	q outbox.Execer
}

// NewPublisher recibe el db.ContextPool: Enqueue escribe por la transaccion del contexto.
func NewPublisher(q outbox.Execer) *Publisher { return &Publisher{q: q} }

func (p *Publisher) EntryAdded(ctx context.Context, e *domain.Entry, reasons []domain.Reason) error {
	return p.enqueue(ctx, SubjectEntryAdded, e.TenantID, entryData(e, reasons))
}

func (p *Publisher) EntryRemoved(ctx context.Context, e *domain.Entry, reasons []domain.Reason) error {
	return p.enqueue(ctx, SubjectEntryRemoved, e.TenantID, entryData(e, reasons))
}

// EntryExpired lleva, ademas de lo que llevan added y removed, expires_at: la caducidad
// que se anuncia, en RFC 3339 con fraccion y en UTC.
func (p *Publisher) EntryExpired(ctx context.Context, e *domain.Entry, reasons []domain.Reason) error {
	if e.ExpiresAt == nil {
		return errExpiryWithoutDate
	}
	data := entryData(e, reasons)
	data["expires_at"] = e.ExpiresAt.UTC().Format(time.RFC3339Nano)
	return p.enqueue(ctx, SubjectEntryExpired, e.TenantID, data)
}

// entryData es el payload comun. reason es la causa que entro, se retiro o caduco; reasons,
// las causas vigentes que le quedan a la direccion despues (vacio si quedo libre, nunca
// null).
func entryData(e *domain.Entry, reasons []domain.Reason) map[string]interface{} {
	remaining := make([]string, len(reasons))
	for i, r := range reasons {
		remaining[i] = string(r)
	}
	return map[string]interface{}{
		"tenant_id": e.TenantID.String(),
		"email":     e.Email,
		"reason":    string(e.Reason),
		"source":    e.Source,
		"reasons":   remaining,
	}
}

// enqueue arma el envelope. UserID lleva a quien actuo cuando la operacion vino de una
// persona (borrado manual): es la pista de auditoria de una reactivacion. Una caducidad la
// anuncia el barrido y va sin persona.
func (p *Publisher) enqueue(ctx context.Context, subject string, tenantID uuid.UUID, data map[string]interface{}) error {
	return outbox.Enqueue(ctx, p.q, subject, events.Event{
		Type:     subject,
		Source:   source,
		TenantID: tenantID.String(),
		UserID:   middleware.GetUserID(ctx),
		Data:     data,
	})
}
