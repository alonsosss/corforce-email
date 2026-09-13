// Package outbox anuncia por la outbox del registro (platform.event_outbox, pkg/outbox) los
// hechos del ciclo de vida de una cuenta que otro servicio necesita con garantia. El evento se
// encola en la misma transaccion que el cambio y el rele que arranca main.go lo entrega al
// stream IDENTITY. Los demas eventos de identity (inicios, fallos, bloqueos) siguen saliendo
// directos al bus: solo los registra audit.
package outbox

import (
	"context"
	"errors"
	"time"

	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/pkg/outbox"
	"github.com/alonsosss/corforce-email/services/identity/internal/domain"
	"github.com/google/uuid"
)

const (
	// StreamName es el stream que identity posee. Lleva un subject por hecho persistente,
	// sin comodin: el resto de identity.* no pasa por JetStream y no se solapa con nadie.
	StreamName = "IDENTITY"
	// SubjectUserDeleted: una cuenta suelta se borro (DELETE /users/{id}). La baja de una
	// empresa entera no lo emite: access-control retira sus roles por empresa en la saga.
	SubjectUserDeleted = "identity.user.deleted"

	// source y el tipo sin el dominio siguen el sobre del resto de eventos de identity, que
	// es el que audit ya registra.
	source          = "identity-service"
	typeUserDeleted = "user.deleted"
)

var errIncompleteDeletion = errors.New("identity: baja sin cuenta o sin empresa")

// Publisher implementa ports.AccountEvents. Recibe el db.ContextPool: Enqueue escribe por la
// transaccion del contexto, que es la del caso de uso.
type Publisher struct {
	exec outbox.Execer
}

func NewPublisher(exec outbox.Execer) *Publisher { return &Publisher{exec: exec} }

// UserDeleted encola identity.user.deleted. Payload: tenant_id y user_id de la cuenta
// borrada y deleted_at en RFC 3339 con fraccion, en UTC. El user_id del sobre es quien la
// borro (vacio si no hubo persona), como en identity.session.revoked_by_admin.
func (p *Publisher) UserDeleted(ctx context.Context, d domain.UserDeletion) error {
	if d.UserID == uuid.Nil || d.TenantID == uuid.Nil {
		return errIncompleteDeletion
	}
	return outbox.Enqueue(ctx, p.exec, SubjectUserDeleted, events.Event{
		Type:     typeUserDeleted,
		Source:   source,
		TenantID: d.TenantID.String(),
		UserID:   actor(d.ActorID),
		Data: map[string]interface{}{
			"tenant_id":  d.TenantID.String(),
			"user_id":    d.UserID.String(),
			"deleted_at": d.DeletedAt.UTC().Format(time.RFC3339Nano),
		},
	})
}

func actor(id uuid.UUID) string {
	if id == uuid.Nil {
		return ""
	}
	return id.String()
}
