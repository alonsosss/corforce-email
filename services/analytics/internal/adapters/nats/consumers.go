// Package nats consume los hechos que alimentan los agregados: los hitos de cada mensaje
// que publica transactional y los cambios de estado que publica campaigns. Un consumidor
// durable por stream, filtrado por comodin: la accion viaja en el tipo del evento.
//
// Los streams los declaran sus duenos (TRANSACTIONAL, CAMPAIGNS). Si uno aun no existe,
// la suscripcion se reintenta cada subscribeRetry sin bloquear el HTTP.
package nats

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/services/analytics/internal/app"
	"github.com/alonsosss/corforce-email/services/analytics/internal/domain"
	"github.com/google/uuid"
	natsgo "github.com/nats-io/nats.go"
	"go.uber.org/zap"
)

const (
	durableTransactional = "analytics-transactional"
	durableCampaigns     = "analytics-campaigns"

	// processTimeout acota resolver la base de la empresa y la transaccion de un evento;
	// queda por debajo del AckWait del bus.
	processTimeout = 15 * time.Second
	subscribeRetry = 10 * time.Second
)

type binding struct {
	subject string
	bind    func() (*natsgo.Subscription, error)
}

// Consumers mantiene las suscripciones durables y traduce cada evento a la ingesta.
type Consumers struct {
	bus      *events.Bus
	uc       *app.UseCase
	tenantDB *db.TenantDB
	logger   *zap.Logger

	mu      sync.Mutex
	subs    []*natsgo.Subscription
	stopped bool
}

func NewConsumers(bus *events.Bus, uc *app.UseCase, tenantDB *db.TenantDB, logger *zap.Logger) *Consumers {
	return &Consumers{bus: bus, uc: uc, tenantDB: tenantDB, logger: logger}
}

func (c *Consumers) bindings() []binding {
	return []binding{
		{subject: "transactional.email.*", bind: func() (*natsgo.Subscription, error) {
			return c.bus.DurableQueueSubscribe("transactional.email.*", durableTransactional, c.onEmailEvent)
		}},
		{subject: "campaigns.campaign.*", bind: func() (*natsgo.Subscription, error) {
			return c.bus.DurableQueueSubscribe("campaigns.campaign.*", durableCampaigns, c.onCampaignEvent)
		}},
	}
}

// Start suscribe en segundo plano y reintenta cada suscripcion que aun no tiene stream
// hasta conseguirlo o hasta que el contexto termine.
func (c *Consumers) Start(ctx context.Context) {
	pending := c.bindings()
	go func() {
		t := time.NewTicker(subscribeRetry)
		defer t.Stop()
		for {
			pending = c.subscribeAll(pending)
			if len(pending) == 0 {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

func (c *Consumers) subscribeAll(pending []binding) []binding {
	var still []binding
	for _, b := range pending {
		sub, err := b.bind()
		if err != nil {
			c.logger.Warn("analytics: sin stream para el subject; se reintentara",
				zap.String("subject", b.subject), zap.Error(err))
			still = append(still, b)
			continue
		}
		c.mu.Lock()
		if c.stopped {
			c.mu.Unlock()
			_ = sub.Drain()
			return nil
		}
		c.subs = append(c.subs, sub)
		c.mu.Unlock()
		c.logger.Info("analytics: suscrito", zap.String("subject", b.subject))
	}
	return still
}

func (c *Consumers) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopped = true
	for _, s := range c.subs {
		_ = s.Drain()
	}
	c.subs = nil
}

// onEmailEvent cuenta un hito de transactional.email.<accion>. Payload:
// {tenant_id, message_id, email | to[], class?, campaign_id?, bounce_type?, occurred_at?,
// test?}. De la direccion solo se conserva el dominio. Un envio de prueba (test: true) se
// confirma sin contarlo; sin el campo, o con otro valor que no sea el booleano true, cuenta.
func (c *Consumers) onEmailEvent(evt events.Event, ack func()) {
	milestone, ok := domain.MilestoneFromAction(actionOf(evt.Type))
	if !ok {
		ack()
		return
	}
	data, _ := evt.Data.(map[string]interface{})
	tenantID, ok := c.tenantOf(evt, str(data["tenant_id"]))
	if !ok {
		ack()
		return
	}
	class, err := domain.EventClass(str(data["class"]))
	if err != nil {
		c.discard(evt, err)
		ack()
		return
	}
	campaignID, err := optionalUUID(data["campaign_id"])
	if err != nil {
		c.discard(evt, err)
		ack()
		return
	}
	email := str(data["email"])
	if email == "" {
		email = firstString(data["to"])
	}
	ev := domain.MessageEvent{
		EventID:         parseUUID(evt.ID),
		TenantID:        tenantID,
		MessageID:       parseUUID(str(data["message_id"])),
		Milestone:       milestone,
		Class:           class,
		CampaignID:      campaignID,
		RecipientDomain: domain.RecipientDomain(email),
		BounceKind:      domain.BounceKindFromProvider(str(data["bounce_type"])),
		Test:            data["test"] == true,
		OccurredAt:      parseTime(str(data["occurred_at"])),
		PublishedAt:     evt.Timestamp,
	}
	if ev.Test {
		c.logger.Debug("analytics: envio de prueba; no se cuenta",
			zap.String("type", evt.Type), zap.String("event_id", evt.ID))
		ack()
		return
	}
	c.process(evt, tenantID, ack, func(ctx context.Context) error {
		_, err := c.uc.IngestMessageEvent(ctx, ev)
		return err
	})
}

// onCampaignEvent registra campaigns.campaign.<accion>. Payload:
// {tenant_id, campaign_id, status, occurred_at?}.
func (c *Consumers) onCampaignEvent(evt events.Event, ack func()) {
	action, ok := domain.CampaignActionFrom(actionOf(evt.Type))
	if !ok {
		ack()
		return
	}
	data, _ := evt.Data.(map[string]interface{})
	tenantID, ok := c.tenantOf(evt, str(data["tenant_id"]))
	if !ok {
		ack()
		return
	}
	ev := domain.CampaignEvent{
		EventID:     parseUUID(evt.ID),
		TenantID:    tenantID,
		CampaignID:  parseUUID(str(data["campaign_id"])),
		Action:      action,
		Status:      str(data["status"]),
		OccurredAt:  parseTime(str(data["occurred_at"])),
		PublishedAt: evt.Timestamp,
	}
	c.process(evt, tenantID, ack, func(ctx context.Context) error {
		_, err := c.uc.IngestCampaignEvent(ctx, ev)
		return err
	})
}

// process resuelve la base de la empresa y aplica la ingesta. Se acka lo procesado y lo
// que nunca podra procesarse (empresa inexistente, evento invalido); lo transitorio queda
// sin ack y JetStream lo reentrega pasado el AckWait, hasta dejarlo en la DLQ.
func (c *Consumers) process(evt events.Event, tenantID uuid.UUID, ack func(), fn func(ctx context.Context) error) {
	ctx, cancel := context.WithTimeout(context.Background(), processTimeout)
	defer cancel()
	pool, err := c.tenantDB.ResolveForTenant(ctx, tenantID.String())
	if err != nil {
		if db.IsUnknownTenant(err) {
			c.logger.Error("analytics: evento de una empresa inexistente; se descarta",
				zap.String("type", evt.Type), zap.String("tenant_id", tenantID.String()))
			ack()
			return
		}
		c.logger.Warn("analytics: empresa sin pool; se reintentara", zap.String("type", evt.Type), zap.Error(err))
		return
	}
	err = fn(db.WithTenant(ctx, pool, tenantID.String()))
	switch {
	case err == nil:
		ack()
	case app.IsPermanent(err):
		c.discard(evt, err)
		ack()
	default:
		c.logger.Warn("analytics: no se pudo contabilizar el evento; se reintentara",
			zap.String("type", evt.Type), zap.String("event_id", evt.ID), zap.Error(err))
	}
}

func (c *Consumers) discard(evt events.Event, err error) {
	c.logger.Error("analytics: evento invalido; se descarta",
		zap.String("type", evt.Type), zap.String("event_id", evt.ID), zap.Error(err))
}

// tenantOf lee la empresa del payload y, si falta, la del sobre.
func (c *Consumers) tenantOf(evt events.Event, raw string) (uuid.UUID, bool) {
	id, err := uuid.Parse(raw)
	if err != nil {
		id, err = uuid.Parse(evt.TenantID)
	}
	if err != nil || id == uuid.Nil {
		c.logger.Error("analytics: evento sin tenant_id; se descarta",
			zap.String("type", evt.Type), zap.String("event_id", evt.ID))
		return uuid.Nil, false
	}
	return id, true
}

func actionOf(eventType string) string {
	return eventType[strings.LastIndexByte(eventType, '.')+1:]
}

func optionalUUID(v interface{}) (*uuid.UUID, error) {
	if v == nil {
		return nil, nil
	}
	s, ok := v.(string)
	if !ok {
		return nil, fmt.Errorf("%w: campaign_id no es texto", domain.ErrInvalidEvent)
	}
	if s == "" {
		return nil, nil
	}
	id, err := uuid.Parse(s)
	if err != nil || id == uuid.Nil {
		return nil, fmt.Errorf("%w: campaign_id no es un uuid", domain.ErrInvalidEvent)
	}
	return &id, nil
}

func parseUUID(s string) uuid.UUID {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil
	}
	return id
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func firstString(v interface{}) string {
	list, _ := v.([]interface{})
	for _, item := range list {
		if s, ok := item.(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func str(v interface{}) string {
	s, _ := v.(string)
	return s
}
