// Package nats consume los hechos de entrega que publica transactional (enviado, rebote,
// queja) y los convierte en contadores idempotentes de la reputacion.
package nats

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/services/reputation/internal/app"
	"github.com/alonsosss/corforce-email/services/reputation/internal/domain"
	"github.com/alonsosss/corforce-email/services/reputation/internal/ports"
	"github.com/google/uuid"
	natsgo "github.com/nats-io/nats.go"
	"go.uber.org/zap"
)

const (
	// handleTimeout acota resolver la base de la empresa, contar el hecho y reevaluar.
	handleTimeout = 15 * time.Second
	// subscribeRetry: el stream TRANSACTIONAL lo declara transactional; si este servicio
	// arranca antes, la suscripcion falla ("no stream matches subject") y se reintenta
	// hasta que exista, sin bloquear el HTTP.
	subscribeRetry = 10 * time.Second
)

type subscription struct {
	subject string
	durable string
}

// Consumer mantiene un consumidor durable por subject (el consumidor de JetStream filtra
// por uno solo).
type Consumer struct {
	bus     *events.Bus
	uc      *app.UseCase
	tenants ports.TenantDirectory
	logger  *zap.Logger

	mu   sync.Mutex
	subs []*natsgo.Subscription
}

func NewConsumer(bus *events.Bus, uc *app.UseCase, tenants ports.TenantDirectory, logger *zap.Logger) *Consumer {
	return &Consumer{bus: bus, uc: uc, tenants: tenants, logger: logger}
}

// Start suscribe en segundo plano y reintenta cada subject que aun no tiene stream hasta
// conseguirlo o hasta que el contexto termine.
func (c *Consumer) Start(ctx context.Context) {
	pending := []subscription{
		{subject: app.SubjectEmailSent, durable: "reputation-sent"},
		{subject: app.SubjectEmailBounced, durable: "reputation-bounced"},
		{subject: app.SubjectEmailComplained, durable: "reputation-complained"},
	}
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

func (c *Consumer) subscribeAll(pending []subscription) []subscription {
	var still []subscription
	for _, s := range pending {
		sub, err := c.bus.DurableQueueSubscribe(s.subject, s.durable, c.handle(s.subject))
		if err != nil {
			c.logger.Warn("reputation: sin stream para el subject; se reintentara",
				zap.String("subject", s.subject), zap.Error(err))
			still = append(still, s)
			continue
		}
		c.mu.Lock()
		c.subs = append(c.subs, sub)
		c.mu.Unlock()
		c.logger.Info("reputation: suscrito", zap.String("subject", s.subject), zap.String("durable", s.durable))
	}
	return still
}

func (c *Consumer) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range c.subs {
		_ = s.Drain()
	}
	c.subs = nil
}

// payload es lo que se lee de Data. Se decodifica a una estructura para tolerar los campos
// que el productor anada; to solo se cuenta.
type payload struct {
	TenantID   string          `json:"tenant_id"`
	Class      string          `json:"class"`
	BounceType string          `json:"bounce_type"`
	To         json.RawMessage `json:"to"`
	Test       bool            `json:"test"`
}

func decode(data interface{}) (payload, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return payload{}, err
	}
	var p payload
	err = json.Unmarshal(raw, &p)
	return p, err
}

// recipients cuenta los destinatarios de un envio; 0 si el campo falta o no es una lista.
func (p payload) recipients() int64 {
	var list []json.RawMessage
	if len(p.To) == 0 || json.Unmarshal(p.To, &list) != nil {
		return 0
	}
	return int64(len(list))
}

// handle devuelve el manejador de un subject. Sin ack, JetStream reentrega; se acka lo
// procesado y tambien lo que nunca va a poder procesarse (payload roto, empresa que no
// existe, clase desconocida): reintentar eso solo envenena la cola.
func (c *Consumer) handle(subject string) func(events.Event, func()) {
	kind, _ := app.KindForSubject(subject)
	return func(evt events.Event, ack func()) {
		logger := c.logger.With(zap.String("subject", subject), zap.String("event_id", evt.ID))
		p, err := decode(evt.Data)
		if err != nil {
			logger.Error("reputation: payload ilegible; se descarta", zap.Error(err))
			ack()
			return
		}
		tenantID, err := uuid.Parse(p.TenantID)
		if err != nil {
			tenantID, err = uuid.Parse(evt.TenantID)
		}
		if err != nil {
			logger.Error("reputation: evento sin tenant_id; se descarta")
			ack()
			return
		}
		class, err := domain.ClassOrDefault(p.Class)
		if err != nil {
			logger.Error("reputation: clase de envio desconocida; se descarta", zap.String("class", p.Class))
			ack()
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), handleTimeout)
		defer cancel()
		tctx, err := c.tenants.Scope(ctx, tenantID)
		if err != nil {
			if errors.Is(err, domain.ErrTenantNotFound) {
				logger.Error("reputation: evento de una empresa inexistente; se descarta",
					zap.String("tenant_id", tenantID.String()))
				ack()
				return
			}
			logger.Warn("reputation: empresa sin pool; se reintentara", zap.Error(err))
			return
		}
		_, err = c.uc.RecordDelivery(tctx, app.DeliveryEvent{
			EventID:    evt.ID,
			TenantID:   tenantID,
			Kind:       kind,
			Class:      class,
			Recipients: p.recipients(),
			BounceType: p.BounceType,
			Test:       p.Test,
			OccurredAt: evt.Timestamp,
		})
		if err != nil {
			if app.IsInputError(err) {
				logger.Error("reputation: evento invalido; se descarta", zap.Error(err))
				ack()
				return
			}
			logger.Warn("reputation: no se pudo registrar el hecho de entrega; se reintentara", zap.Error(err))
			return
		}
		ack()
	}
}
