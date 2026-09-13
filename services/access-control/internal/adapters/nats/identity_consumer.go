// Package nats consume los eventos de otros servicios que cambian lo que access-control guarda.
package nats

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	// IdentityStreamName e IdentitySubjectUserDeleted repiten la declaracion de identity,
	// dueno del stream: el consumidor lo asegura antes de suscribirse para no depender de que
	// identity arranque primero. Con los mismos subjects, EnsureStream no cambia nada.
	IdentityStreamName         = "IDENTITY"
	IdentitySubjectUserDeleted = "identity.user.deleted"

	// userDeletedDurable conserva sus acuses entre reinicios.
	userDeletedDurable = "access-control-user-deleted"

	subscribeRetry = 5 * time.Second
	handleTimeout  = 15 * time.Second
)

// AccountForgetter es lo que el consumidor necesita del caso de uso.
type AccountForgetter interface {
	ForgetDeletedUser(ctx context.Context, tenantID, userID uuid.UUID) (int64, error)
}

// IdentityConsumer retira las asignaciones de roles de las cuentas que identity borra.
type IdentityConsumer struct {
	bus      *events.Bus
	accounts AccountForgetter
	logger   *zap.Logger
}

func NewIdentityConsumer(bus *events.Bus, accounts AccountForgetter, logger *zap.Logger) *IdentityConsumer {
	return &IdentityConsumer{bus: bus, accounts: accounts, logger: logger}
}

// Run bloquea hasta suscribirse o hasta que el contexto se cancele; si NATS no responde
// reintenta. Mientras tanto las bajas esperan en el stream, que las guarda 7 dias.
func (c *IdentityConsumer) Run(ctx context.Context) {
	if c.bus == nil {
		return
	}
	for {
		err := c.bus.EnsureStream(IdentityStreamName, []string{IdentitySubjectUserDeleted})
		if err == nil {
			_, err = c.bus.DurableQueueSubscribe(IdentitySubjectUserDeleted, userDeletedDurable, c.Handle)
		}
		if err == nil {
			c.logger.Info("suscrito a las bajas de cuentas", zap.String("consumer", userDeletedDurable))
			return
		}
		c.logger.Warn("no se pudo suscribir a las bajas de cuentas; se reintenta", zap.Error(err))
		select {
		case <-ctx.Done():
			return
		case <-time.After(subscribeRetry):
		}
	}
}

// Handle es idempotente: borra lo que quede y confirma aunque no quede nada (una entrega
// repetida, una cuenta sin roles o una empresa cuya baja ya los retiro). Un evento ilegible se
// confirma para no reentregarse sin fin; un fallo de la base no, y se reentrega hasta agotar
// sus entregas, cuando pkg/events lo guarda en EVENTS_DLQ.
func (c *IdentityConsumer) Handle(evt events.Event, ack func()) {
	data, _ := evt.Data.(map[string]any)
	rawUser, _ := data["user_id"].(string)
	rawTenant, _ := data["tenant_id"].(string)
	userID, userErr := uuid.Parse(rawUser)
	tenantID, tenantErr := uuid.Parse(rawTenant)
	if userErr != nil || tenantErr != nil || userID == uuid.Nil || tenantID == uuid.Nil {
		c.logger.Warn("baja de cuenta sin user_id o tenant_id validos; se descarta", zap.String("event_id", evt.ID))
		ack()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), handleTimeout)
	defer cancel()
	removed, err := c.accounts.ForgetDeletedUser(ctx, tenantID, userID)
	if err != nil {
		c.logger.Error("roles de la cuenta borrada sin retirar; se reentregara", zap.String("event_id", evt.ID),
			zap.String("user_id", userID.String()), zap.Error(err))
		return
	}
	c.logger.Info("roles de la cuenta borrada retirados", zap.String("user_id", userID.String()),
		zap.String("tenant_id", tenantID.String()), zap.Int64("asignaciones", removed))
	ack()
}
