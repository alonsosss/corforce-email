// Package nats contiene los consumidores durables de automations: el del doble opt-in
// (contacts.consent.requested) y uno por disparador de flujo.
package nats

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/services/automations/internal/app"
	"github.com/alonsosss/corforce-email/services/automations/internal/domain"
	"github.com/google/uuid"
	natsgo "github.com/nats-io/nats.go"
	"go.uber.org/zap"
)

// Consumidores durables. Los subjects se escriben literales en las llamadas por
// convencion del repositorio (los guardianes de streams y de eventos los leen ahi).
const (
	doiDurable            = "automations-doi"
	contactCreatedDurable = "automations-contact-created"
	consentGrantedDurable = "automations-consent-granted"
	emailClickedDurable   = "automations-email-clicked"

	// subscribeRetry: si el stream aun no existe, la suscripcion se reintenta sin
	// bloquear el HTTP.
	subscribeRetry = 10 * time.Second
	// handleTimeout acota cada entrega; queda por debajo del AckWait del bus (90s).
	handleTimeout = 60 * time.Second
	// classMarketing: solo un clic en un correo de marketing (con contacto) dispara.
	classMarketing = "marketing"
)

// EnsureStreams declara el stream propio y los que consume, con los mismos subjects que
// sus productores (EnsureStream une subjects y nunca quita los ajenos).
func EnsureStreams(bus *events.Bus) error {
	if err := bus.EnsureStream("AUTOMATIONS", []string{"automations.>"}); err != nil {
		return err
	}
	if err := bus.EnsureStream("CONTACTS", []string{"contacts.>"}); err != nil {
		return err
	}
	return bus.EnsureStream("TRANSACTIONAL", []string{"transactional.>"})
}

type Consumers struct {
	bus      *events.Bus
	uc       *app.UseCase
	tenantDB *db.TenantDB
	logger   *zap.Logger

	mu   sync.Mutex
	subs []*natsgo.Subscription
}

func NewConsumers(bus *events.Bus, uc *app.UseCase, tenantDB *db.TenantDB, logger *zap.Logger) *Consumers {
	return &Consumers{bus: bus, uc: uc, tenantDB: tenantDB, logger: logger}
}

// Start suscribe cada consumidor en segundo plano.
func (c *Consumers) Start(ctx context.Context) {
	c.keepSubscribing(ctx, doiDurable, func() (*natsgo.Subscription, error) {
		return c.bus.DurableQueueSubscribe("contacts.consent.requested", doiDurable, c.handleConsentRequested)
	})
	c.keepSubscribing(ctx, contactCreatedDurable, func() (*natsgo.Subscription, error) {
		return c.bus.DurableQueueSubscribe("contacts.contact.created", contactCreatedDurable, c.handleContactCreated)
	})
	c.keepSubscribing(ctx, consentGrantedDurable, func() (*natsgo.Subscription, error) {
		return c.bus.DurableQueueSubscribe("contacts.consent.granted", consentGrantedDurable, c.handleConsentGranted)
	})
	c.keepSubscribing(ctx, emailClickedDurable, func() (*natsgo.Subscription, error) {
		return c.bus.DurableQueueSubscribe("transactional.email.clicked", emailClickedDurable, c.handleEmailClicked)
	})
}

func (c *Consumers) keepSubscribing(ctx context.Context, durable string, subscribe func() (*natsgo.Subscription, error)) {
	go func() {
		t := time.NewTicker(subscribeRetry)
		defer t.Stop()
		for {
			sub, err := subscribe()
			if err == nil {
				c.mu.Lock()
				c.subs = append(c.subs, sub)
				c.mu.Unlock()
				c.logger.Info("automations: consumidor suscrito", zap.String("durable", durable))
				return
			}
			c.logger.Warn("automations: consumidor sin suscribir; se reintentara", zap.String("durable", durable), zap.Error(err))
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

func (c *Consumers) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range c.subs {
		_ = s.Drain()
	}
	c.subs = nil
}

// tenantContext resuelve la base de la empresa. ok=false con ack=true cuando la empresa no
// existe (el evento nunca se podra procesar); ok=false con ack=false cuando la base no
// responde ahora.
func (c *Consumers) tenantContext(tenantID uuid.UUID) (ctx context.Context, cancel context.CancelFunc, ok, ack bool) {
	ctx, cancel = context.WithTimeout(context.Background(), handleTimeout)
	pool, err := c.tenantDB.ResolveForTenant(ctx, tenantID.String())
	if err != nil {
		cancel()
		if db.IsUnknownTenant(err) {
			c.logger.Error("automations: evento de una empresa inexistente; se descarta", zap.String("tenant_id", tenantID.String()))
			return nil, nil, false, true
		}
		c.logger.Warn("automations: empresa sin pool; se reintentara", zap.String("tenant_id", tenantID.String()), zap.Error(err))
		return nil, nil, false, false
	}
	return db.WithTenant(ctx, pool, tenantID.String()), cancel, true, false
}

// handleConsentRequested: el enlace de confirmacion es una credencial; nunca se escribe en
// el log ni el evento entero.
func (c *Consumers) handleConsentRequested(evt events.Event, ack func()) {
	data, _ := evt.Data.(map[string]interface{})
	req := domain.ConsentRequest{
		EventID:    evt.ID,
		TenantID:   eventTenant(evt, data),
		ContactID:  parseID(data["contact_id"]),
		Email:      str(data["email"]),
		ConfirmURL: str(data["confirm_url"]),
		FirstName:  str(data["first_name"]),
	}
	if req.TenantID == uuid.Nil {
		c.logger.Error("automations: peticion de doble opt-in sin empresa; se descarta", zap.String("event_id", evt.ID))
		ack()
		return
	}
	ctx, cancel, ok, doAck := c.tenantContext(req.TenantID)
	if !ok {
		if doAck {
			ack()
		}
		return
	}
	defer cancel()
	status, err := c.uc.HandleConsentRequested(ctx, req)
	if err != nil {
		if errors.Is(err, domain.ErrInvalidInput) {
			c.logger.Error("automations: peticion de doble opt-in invalida; se descarta",
				zap.String("event_id", evt.ID), zap.String("tenant_id", req.TenantID.String()), zap.Error(err))
			ack()
			return
		}
		c.logger.Warn("automations: correo del doble opt-in pendiente; se reintentara",
			zap.String("event_id", evt.ID), zap.String("tenant_id", req.TenantID.String()), zap.Error(err))
		return
	}
	c.logger.Info("automations: peticion de doble opt-in atendida",
		zap.String("event_id", evt.ID), zap.String("tenant_id", req.TenantID.String()),
		zap.String("contact_id", req.ContactID.String()), zap.String("status", string(status)))
	ack()
}

func (c *Consumers) handleContactCreated(evt events.Event, ack func()) {
	data, _ := evt.Data.(map[string]interface{})
	c.enroll(evt, ack, domain.TriggerEvent{
		EventID: evt.ID, TenantID: eventTenant(evt, data), Type: domain.TriggerContactCreated,
		ContactID: parseID(data["contact_id"]),
	})
}

// handleConsentGranted: solo el consentimiento de marketing dispara. Un evento sin
// proposito se toma como de marketing, que es el unico que contacts registra hoy.
func (c *Consumers) handleConsentGranted(evt events.Event, ack func()) {
	data, _ := evt.Data.(map[string]interface{})
	if purpose := str(data["purpose"]); purpose != "" && purpose != domain.PurposeMarketing {
		ack()
		return
	}
	c.enroll(evt, ack, domain.TriggerEvent{
		EventID: evt.ID, TenantID: eventTenant(evt, data), Type: domain.TriggerConsentGranted,
		ContactID: parseID(data["contact_id"]),
	})
}

// handleEmailClicked: solo los clics en marketing llevan contacto; los transaccionales se
// confirman sin mas.
func (c *Consumers) handleEmailClicked(evt events.Event, ack func()) {
	data, _ := evt.Data.(map[string]interface{})
	if class := str(data["class"]); class != classMarketing {
		ack()
		return
	}
	ev := domain.TriggerEvent{
		EventID: evt.ID, TenantID: eventTenant(evt, data), Type: domain.TriggerEmailClicked,
		ContactID: parseID(data["contact_id"]),
	}
	if id := parseID(data["campaign_id"]); id != uuid.Nil {
		ev.CampaignID = &id
	}
	c.enroll(evt, ack, ev)
}

func (c *Consumers) enroll(evt events.Event, ack func(), ev domain.TriggerEvent) {
	if ev.TenantID == uuid.Nil || ev.ContactID == uuid.Nil {
		ack()
		return
	}
	ctx, cancel, ok, doAck := c.tenantContext(ev.TenantID)
	if !ok {
		if doAck {
			ack()
		}
		return
	}
	defer cancel()
	entered, err := c.uc.HandleTrigger(ctx, ev)
	if err != nil {
		if errors.Is(err, domain.ErrInvalidInput) {
			ack()
			return
		}
		c.logger.Warn("automations: evento de disparo no procesado; se reintentara",
			zap.String("type", evt.Type), zap.String("event_id", evt.ID), zap.Error(err))
		return
	}
	if entered > 0 {
		c.logger.Info("automations: contacto en flujos", zap.String("tenant_id", ev.TenantID.String()),
			zap.String("trigger", string(ev.Type)), zap.Int("runs", entered))
	}
	ack()
}

func eventTenant(evt events.Event, data map[string]interface{}) uuid.UUID {
	if id := parseID(data["tenant_id"]); id != uuid.Nil {
		return id
	}
	id, _ := uuid.Parse(evt.TenantID)
	return id
}

func parseID(v interface{}) uuid.UUID {
	id, err := uuid.Parse(str(v))
	if err != nil {
		return uuid.Nil
	}
	return id
}

func str(v interface{}) string {
	s, _ := v.(string)
	return s
}
