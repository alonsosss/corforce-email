// Package nats borra los trabajos de migracion de un buzon cuando mail-directory lo da de baja.
package nats

import (
	"context"
	"errors"
	"time"

	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	// StreamName y StreamSubjects repiten la declaracion de mail-directory, dueno de mail.>: el
	// consumidor asegura el stream antes de suscribirse para no depender de que el productor
	// arranque primero.
	StreamName            = "MAIL_DIRECTORY"
	StreamSubjects        = "mail.>"
	SubjectMailboxDeleted = "mail.mailbox.deleted"
	ConsumerName          = "mail-migration-mailbox-deleted"

	subscribeRetry = 5 * time.Second
	handleTimeout  = 30 * time.Second
)

// Purger es lo que el consumidor necesita del caso de uso.
type Purger interface {
	PurgeMailbox(ctx context.Context, tenantID, mailboxID uuid.UUID) (int, error)
}

// Consumer aplica mail.mailbox.deleted a la base de la empresa del evento y a ninguna otra. Un
// evento que no se pudo aplicar se deja sin confirmar: JetStream lo reentrega y, agotadas sus
// entregas, pasa a EVENTS_DLQ, que alerta. Aplicarlo dos veces deja lo mismo que una.
type Consumer struct {
	bus    *events.Bus
	purger Purger
	logger *zap.Logger
}

func NewConsumer(bus *events.Bus, purger Purger, logger *zap.Logger) *Consumer {
	return &Consumer{bus: bus, purger: purger, logger: logger}
}

// Run bloquea hasta suscribirse o hasta que el contexto se cancele; si NATS no responde
// reintenta. Mientras no haya suscripcion los trabajos de un buzon borrado siguen en la base de
// su empresa, y el durable los recoge al suscribirse porque el stream los conserva.
func (c *Consumer) Run(ctx context.Context) {
	if c.bus == nil {
		return
	}
	for {
		err := c.bus.EnsureStream(StreamName, []string{StreamSubjects})
		if err == nil {
			_, err = c.bus.DurableQueueSubscribe(SubjectMailboxDeleted, ConsumerName, c.Handle)
		}
		if err == nil {
			c.logger.Info("mail-migration: suscrito a los buzones borrados", zap.String("consumer", ConsumerName))
			return
		}
		c.logger.Warn("mail-migration: no se pudo suscribir a los buzones borrados; se reintenta", zap.Error(err))
		select {
		case <-ctx.Done():
			return
		case <-time.After(subscribeRetry):
		}
	}
}

func (c *Consumer) Handle(evt events.Event, ack func()) {
	data, _ := evt.Data.(map[string]any)
	rawTenant, _ := data["tenant_id"].(string)
	rawMailbox, _ := data["id"].(string)
	tenantID, tenantErr := uuid.Parse(rawTenant)
	mailboxID, mailboxErr := uuid.Parse(rawMailbox)
	if tenantErr != nil || mailboxErr != nil || tenantID == uuid.Nil || mailboxID == uuid.Nil ||
		(evt.TenantID != "" && evt.TenantID != tenantID.String()) {
		c.logger.Error("mail-migration: evento de buzon borrado sin empresa y buzon coherentes; queda para EVENTS_DLQ",
			zap.String("event_id", evt.ID))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), handleTimeout)
	defer cancel()
	removed, err := c.purger.PurgeMailbox(ctx, tenantID, mailboxID)
	switch {
	case err == nil:
		c.logger.Info("mail-migration: trabajos del buzon borrado retirados",
			zap.String("tenant_id", tenantID.String()), zap.String("mailbox_id", mailboxID.String()), zap.Int("jobs", removed))
	case errors.Is(err, domain.ErrTenantUnknown):
		c.logger.Warn("mail-migration: evento de una empresa que ya no existe; no hay nada que retirar",
			zap.String("tenant_id", tenantID.String()), zap.String("event_id", evt.ID))
	default:
		c.logger.Warn("mail-migration: trabajos del buzon borrado no retirados; se reentregara",
			zap.String("tenant_id", tenantID.String()), zap.String("mailbox_id", mailboxID.String()), zap.Error(err))
		return
	}
	ack()
}
