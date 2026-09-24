package nats

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/services/contacts/internal/app"
	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/google/uuid"
	natsgo "github.com/nats-io/nats.go"
	"go.uber.org/zap"
)

// Durables de la proyeccion de interaccion: uno por subject, porque el consumidor de
// JetStream filtra por un unico subject.
const (
	engagementDeliveredDurable = "contacts-engagement-delivered"
	engagementOpenedDurable    = "contacts-engagement-opened"
	engagementClickedDurable   = "contacts-engagement-clicked"
	// classMarketing: solo el marketing (campanas y flujos) lleva contacto y campana.
	classMarketing = "marketing"
)

// EngagementWorker proyecta transactional.email.delivered, .opened y .clicked en
// contacts.engagement. Es idempotente sin registrar eventos: la fila guarda la recepcion
// mas antigua y la apertura y el clic mas recientes, asi que una reentrega no cambia nada.
type EngagementWorker struct {
	bus      *events.Bus
	uc       *app.UseCase
	tenantDB *db.TenantDB
	logger   *zap.Logger

	mu   sync.Mutex
	subs []*natsgo.Subscription
}

func NewEngagementWorker(bus *events.Bus, uc *app.UseCase, tenantDB *db.TenantDB, logger *zap.Logger) *EngagementWorker {
	return &EngagementWorker{bus: bus, uc: uc, tenantDB: tenantDB, logger: logger}
}

// Start suscribe cada durable en segundo plano y reintenta mientras falte el stream.
func (w *EngagementWorker) Start(ctx context.Context) {
	w.keepSubscribing(ctx, engagementDeliveredDurable, func() (*natsgo.Subscription, error) {
		return w.bus.DurableQueueSubscribe("transactional.email.delivered", engagementDeliveredDurable, w.handle(domain.EngagementDelivered))
	})
	w.keepSubscribing(ctx, engagementOpenedDurable, func() (*natsgo.Subscription, error) {
		return w.bus.DurableQueueSubscribe("transactional.email.opened", engagementOpenedDurable, w.handle(domain.EngagementOpened))
	})
	w.keepSubscribing(ctx, engagementClickedDurable, func() (*natsgo.Subscription, error) {
		return w.bus.DurableQueueSubscribe("transactional.email.clicked", engagementClickedDurable, w.handle(domain.EngagementClicked))
	})
}

func (w *EngagementWorker) keepSubscribing(ctx context.Context, durable string, subscribe func() (*natsgo.Subscription, error)) {
	go func() {
		t := time.NewTicker(subscribeRetry)
		defer t.Stop()
		for {
			sub, err := subscribe()
			if err == nil {
				w.mu.Lock()
				w.subs = append(w.subs, sub)
				w.mu.Unlock()
				w.logger.Info("contacts: suscrito", zap.String("durable", durable))
				return
			}
			w.logger.Warn("contacts: consumidor de interaccion sin suscribir; se reintentara",
				zap.String("durable", durable), zap.Error(err))
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

func (w *EngagementWorker) Stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, s := range w.subs {
		_ = s.Drain()
	}
	w.subs = nil
}

// engagementEvent interpreta el payload. ok=false: el evento no es de interaccion con un
// envio de marketing a un contacto (transaccional, prueba, sin campana o sin contacto) y
// se confirma sin hacer nada.
func engagementEvent(evt events.Event, data map[string]interface{}, kind domain.EngagementKind) (domain.EngagementEvent, bool) {
	if str(data["class"]) != classMarketing {
		return domain.EngagementEvent{}, false
	}
	if test, _ := data["test"].(bool); test {
		return domain.EngagementEvent{}, false
	}
	tenantID, err := uuid.Parse(str(data["tenant_id"]))
	if err != nil {
		tenantID, _ = uuid.Parse(evt.TenantID)
	}
	contactID, _ := uuid.Parse(str(data["contact_id"]))
	campaignID, _ := uuid.Parse(str(data["campaign_id"]))
	if tenantID == uuid.Nil || contactID == uuid.Nil || campaignID == uuid.Nil {
		return domain.EngagementEvent{}, false
	}
	at, err := time.Parse(time.RFC3339, str(data["occurred_at"]))
	if err != nil {
		at = evt.Timestamp
	}
	return domain.EngagementEvent{TenantID: tenantID, ContactID: contactID, CampaignID: campaignID, Kind: kind, OccurredAt: at}, true
}

// handle: se acka lo aplicado y lo que nunca se podra aplicar (evento ajeno, empresa
// inexistente); un fallo de la base se deja sin ack para que JetStream lo reentregue.
func (w *EngagementWorker) handle(kind domain.EngagementKind) func(events.Event, func()) {
	return func(evt events.Event, ack func()) {
		data, _ := evt.Data.(map[string]interface{})
		ev, ok := engagementEvent(evt, data, kind)
		if !ok {
			ack()
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), resolveTimeout)
		defer cancel()
		pool, err := w.tenantDB.ResolveForTenant(ctx, ev.TenantID.String())
		if err != nil {
			if db.IsUnknownTenant(err) {
				w.logger.Error("contacts: interaccion de una empresa inexistente; se descarta",
					zap.String("tenant_id", ev.TenantID.String()))
				ack()
				return
			}
			w.logger.Warn("contacts: empresa sin pool; se reintentara", zap.Error(err))
			return
		}
		ctx = db.WithTenant(ctx, pool, ev.TenantID.String())
		if _, err := w.uc.RecordEngagement(ctx, ev); err != nil {
			if errors.Is(err, domain.ErrInvalidEngagement) {
				ack()
				return
			}
			w.logger.Warn("contacts: interaccion sin registrar; se reintentara",
				zap.String("kind", string(kind)), zap.String("tenant_id", ev.TenantID.String()), zap.Error(err))
			return
		}
		ack()
	}
}
