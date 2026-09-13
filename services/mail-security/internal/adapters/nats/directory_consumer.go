package nats

import (
	"context"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/ports"
	"go.uber.org/zap"
)

// Reintento de la suscripcion mientras mail-directory no haya declarado su stream.
const subscribeRetry = 30 * time.Second

// DirectoryConsumer sigue los eventos del directorio (mail.>) y mantiene al dia las
// claves de Redis que dependen de el. El stream MAIL_DIRECTORY lo declara
// mail-directory; aqui solo se declara el consumidor durable. Si el stream aun no
// existe, se reintenta: el servicio arranca igual y la reconciliacion periodica cubre
// el hueco.
type DirectoryConsumer struct {
	bus    *events.Bus
	sync   *app.RedisSync
	policy ports.PolicyReader
	// withPool deja en el contexto el pool de la celda: el handler corre fuera de
	// cualquier peticion HTTP.
	withPool func(context.Context) context.Context
	logger   *zap.Logger
}

func NewDirectoryConsumer(bus *events.Bus, sync *app.RedisSync, policy ports.PolicyReader, withPool func(context.Context) context.Context, logger *zap.Logger) *DirectoryConsumer {
	return &DirectoryConsumer{bus: bus, sync: sync, policy: policy, withPool: withPool, logger: logger}
}

// Run bloquea hasta suscribirse o hasta que el contexto se cancele.
func (c *DirectoryConsumer) Run(ctx context.Context) {
	if c.bus == nil {
		return
	}
	for {
		_, err := c.bus.DurableQueueSubscribe(domain.DirectorySubjectPattern, domain.DirectoryConsumer, c.handle)
		if err == nil {
			c.logger.Info("suscrito a los eventos del directorio", zap.String("consumer", domain.DirectoryConsumer))
			return
		}
		c.logger.Warn("sin stream del directorio todavia; se reintenta", zap.Error(err))
		select {
		case <-ctx.Done():
			return
		case <-time.After(subscribeRetry):
		}
	}
}

// handle es idempotente: lee el estado real del directorio, no el del evento. Un
// evento que no se puede procesar por un fallo transitorio se deja sin ack para que se
// reentregue; uno que no entiende se confirma para no girar para siempre.
func (c *DirectoryConsumer) handle(evt events.Event, ack func()) {
	ctx, cancel := context.WithTimeout(c.withPool(context.Background()), 30*time.Second)
	defer cancel()

	// Payloads de mail-directory: mail.domain.* lleva domain; mail.alias_domain.* lleva
	// alias_domain y target_domain; mail.mailbox.* lleva username. Si falta la clave se
	// recalcula DOMAIN_MAP entero, que tambien es correcto.
	data, _ := evt.Data.(map[string]any)
	var err error
	switch {
	case strings.HasPrefix(evt.Type, "mail.domain."):
		err = c.refreshDomainFrom(ctx, data, "domain")
	case strings.HasPrefix(evt.Type, "mail.alias_domain."):
		err = c.refreshDomainFrom(ctx, data, "alias_domain")
	case evt.Type == "mail.mailbox.deleted":
		if username, ok := data["username"].(string); ok && username != "" {
			username = strings.ToLower(username)
			if err = c.policy.DeleteMailboxTagsByUsername(ctx, username); err == nil {
				err = c.sync.RemoveMailboxTags(ctx, username)
			}
		}
	}
	if err != nil {
		c.logger.Error("evento del directorio no aplicado; se reentregara", zap.String("subject", evt.Type), zap.Error(err))
		return
	}
	ack()
}

func (c *DirectoryConsumer) refreshDomainFrom(ctx context.Context, data map[string]any, key string) error {
	if name, ok := data[key].(string); ok && name != "" {
		return c.sync.RefreshDomain(ctx, strings.ToLower(name))
	}
	return c.sync.ReconcileDomains(ctx)
}
