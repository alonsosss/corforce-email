// Package nats consume los eventos de entrega de transactional que alimentan las
// estadisticas de cada campana.
package nats

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/services/campaigns/internal/app"
	"github.com/alonsosss/corforce-email/services/campaigns/internal/domain"
	"github.com/google/uuid"
	natsgo "github.com/nats-io/nats.go"
	"go.uber.org/zap"
)

const (
	statsDurable = "campaigns-stats"
	// subscribeRetry: el stream TRANSACTIONAL lo declara su dueno; si este servicio
	// arranca antes, la suscripcion falla y se reintenta sin bloquear el HTTP.
	subscribeRetry = 10 * time.Second
	handleTimeout  = 15 * time.Second
	// classMarketing: solo el correo de marketing pertenece a una campana.
	classMarketing = "marketing"
)

type StatsConsumer struct {
	bus      *events.Bus
	uc       *app.UseCase
	tenantDB *db.TenantDB
	logger   *zap.Logger

	mu  sync.Mutex
	sub *natsgo.Subscription
}

func NewStatsConsumer(bus *events.Bus, uc *app.UseCase, tenantDB *db.TenantDB, logger *zap.Logger) *StatsConsumer {
	return &StatsConsumer{bus: bus, uc: uc, tenantDB: tenantDB, logger: logger}
}

// Start suscribe en segundo plano y reintenta hasta conseguirlo o hasta que el
// contexto termine.
func (c *StatsConsumer) Start(ctx context.Context) {
	go func() {
		t := time.NewTicker(subscribeRetry)
		defer t.Stop()
		for {
			sub, err := c.bus.DurableQueueSubscribe("transactional.email.>", statsDurable, c.handle)
			if err == nil {
				c.mu.Lock()
				c.sub = sub
				c.mu.Unlock()
				c.logger.Info("campaigns: suscrito a los eventos de entrega", zap.String("durable", statsDurable))
				return
			}
			c.logger.Warn("campaigns: sin stream de eventos de entrega; se reintentara", zap.Error(err))
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

func (c *StatsConsumer) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sub != nil {
		_ = c.sub.Drain()
		c.sub = nil
	}
}

// handle: sin ack, JetStream reentrega. Se acka lo contado y tambien lo que nunca va a
// poder contarse (evento sin campana, empresa o campana inexistentes, payload roto):
// reintentarlo solo envenena la cola.
func (c *StatsConsumer) handle(evt events.Event, ack func()) {
	ev, ok := parseDeliveryEvent(evt)
	if !ok {
		ack()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), handleTimeout)
	defer cancel()
	pool, err := c.tenantDB.ResolveForTenant(ctx, ev.TenantID.String())
	if err != nil {
		if db.IsUnknownTenant(err) {
			c.logger.Error("campaigns: evento de una empresa inexistente; se descarta",
				zap.String("type", evt.Type), zap.String("tenant_id", ev.TenantID.String()))
			ack()
			return
		}
		c.logger.Warn("campaigns: empresa sin pool; se reintentara", zap.Error(err))
		return
	}
	ctx = db.WithTenant(ctx, pool, ev.TenantID.String())

	if _, err := c.uc.RecordDeliveryEvent(ctx, ev); err != nil {
		if errors.Is(err, domain.ErrCampaignNotFound) || errors.Is(err, domain.ErrInvalidCampaign) {
			c.logger.Warn("campaigns: evento de entrega que no se puede contar; se descarta",
				zap.String("type", evt.Type), zap.String("event_id", evt.ID),
				zap.String("campaign_id", ev.CampaignID.String()), zap.Error(err))
			ack()
			return
		}
		c.logger.Warn("campaigns: estadistica no registrada; se reintentara",
			zap.String("type", evt.Type), zap.String("event_id", evt.ID), zap.Error(err))
		return
	}
	ack()
}

// parseDeliveryEvent se queda con los eventos que cuentan para una campana: de un tipo
// con contador, de clase marketing, con campaign_id y con un contact_id que no sea el
// sintetico de un envio de prueba (domain.IsTestContact).
func parseDeliveryEvent(evt events.Event) (domain.DeliveryEvent, bool) {
	kind, ok := domain.ParseDeliveryKind(evt.Type)
	if !ok {
		return domain.DeliveryEvent{}, false
	}
	data, _ := evt.Data.(map[string]interface{})
	if class := str(data["class"]); class != "" && class != classMarketing {
		return domain.DeliveryEvent{}, false
	}
	campaignID, err := uuid.Parse(str(data["campaign_id"]))
	if err != nil {
		return domain.DeliveryEvent{}, false
	}
	contactID, err := uuid.Parse(str(data["contact_id"]))
	if err != nil || domain.IsTestContact(campaignID, contactID, recipientEmail(data)) {
		return domain.DeliveryEvent{}, false
	}
	tenantID, err := uuid.Parse(str(data["tenant_id"]))
	if err != nil {
		if tenantID, err = uuid.Parse(evt.TenantID); err != nil {
			return domain.DeliveryEvent{}, false
		}
	}
	ev := domain.DeliveryEvent{
		EventID: evt.ID, TenantID: tenantID, CampaignID: campaignID, ContactID: &contactID, Kind: kind,
		OccurredAt: occurredAt(data, evt.Timestamp),
	}
	if id, err := uuid.Parse(str(data["message_id"])); err == nil {
		ev.MessageID = &id
	}
	return ev, true
}

// recipientEmail es la direccion del mensaje: email en los eventos por destinatario, o
// el unico elemento de to en sent y failed (un mensaje de marketing tiene uno solo).
func recipientEmail(data map[string]interface{}) string {
	if e := str(data["email"]); e != "" {
		return e
	}
	if list, _ := data["to"].([]interface{}); len(list) == 1 {
		return str(list[0])
	}
	return ""
}

// occurredAt prefiere el momento del hecho que publica transactional; el envelope lleva
// la hora de publicacion, que con la outbox puede ser posterior.
func occurredAt(data map[string]interface{}, fallback time.Time) time.Time {
	if t, err := time.Parse(time.RFC3339Nano, str(data["occurred_at"])); err == nil {
		return t
	}
	return fallback
}

func str(v interface{}) string {
	s, _ := v.(string)
	return s
}
