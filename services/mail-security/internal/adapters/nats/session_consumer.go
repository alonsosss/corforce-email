package nats

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"go.uber.org/zap"
)

// sessionHandleTimeout acota una revocacion (lectura del directorio y llamada a Dovecot); queda
// muy por debajo del plazo de acuse de JetStream (90 s), que la reentregaria mientras corre.
const sessionHandleTimeout = 30 * time.Second

// SessionConsumer sigue los eventos de buzon del directorio (mail.mailbox.>) con su propio
// consumidor durable y retira de Dovecot la credencial que el buzon ya no puede usar. Un fallo
// deja el evento sin confirmar: JetStream lo reentrega y, agotadas las entregas, lo guarda en
// EVENTS_DLQ (pkg/events).
type SessionConsumer struct {
	bus      *events.Bus
	revoker  *app.SessionRevoker
	withPool func(context.Context) context.Context
	logger   *zap.Logger
}

func NewSessionConsumer(bus *events.Bus, revoker *app.SessionRevoker, withPool func(context.Context) context.Context, logger *zap.Logger) *SessionConsumer {
	return &SessionConsumer{bus: bus, revoker: revoker, withPool: withPool, logger: logger}
}

// Run bloquea hasta suscribirse o hasta que el contexto se cancele. Declara el stream del
// directorio como DirectoryConsumer, por el mismo motivo.
func (c *SessionConsumer) Run(ctx context.Context) {
	if c.bus == nil {
		return
	}
	for {
		err := c.bus.EnsureStream(domain.DirectoryStreamName, []string{domain.DirectorySubjectPattern})
		if err == nil {
			_, err = c.bus.DurableQueueSubscribe(domain.MailboxSubjectPattern, domain.SessionsConsumer, c.handle)
		}
		if err == nil {
			c.logger.Info("suscrito a los eventos de buzon para la revocacion en Dovecot", zap.String("consumer", domain.SessionsConsumer))
			return
		}
		c.logger.Warn("no se pudo suscribir a los eventos de buzon; se reintenta", zap.Error(err))
		select {
		case <-ctx.Done():
			return
		case <-time.After(subscribeRetry):
		}
	}
}

// handle confirma el evento solo cuando Dovecot aplico la revocacion. Uno sin username valido se
// confirma para no girar para siempre, y queda como error: esa revocacion no se hizo.
func (c *SessionConsumer) handle(evt events.Event, ack func()) {
	change, ok := domain.MailboxChangeOf(evt.Type)
	if !ok {
		ack()
		return
	}
	data, _ := evt.Data.(map[string]any)
	raw, _ := data["username"].(string)
	username, err := domain.NormalizeSessionUser(raw)
	if err != nil {
		c.logger.Error("evento de buzon sin username valido: no se revoca nada en Dovecot",
			zap.String("subject", evt.Type), zap.String("event_id", evt.ID))
		ack()
		return
	}
	ctx, cancel := context.WithTimeout(c.withPool(context.Background()), sessionHandleTimeout)
	defer cancel()
	if err := c.revoker.Revoke(ctx, change, username); err != nil {
		c.logger.Error("revocacion en Dovecot no aplicada; se reentregara", zap.String("subject", evt.Type),
			zap.String("username", username), zap.Error(err))
		return
	}
	ack()
}
