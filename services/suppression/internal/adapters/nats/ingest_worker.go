// Package nats consume los hechos que alimentan la lista: los rebotes y quejas que
// transactional recibe del proveedor, y los nuevos consentimientos que contacts registra.
package nats

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/services/suppression/internal/app"
	"github.com/google/uuid"
	natsgo "github.com/nats-io/nats.go"
	"go.uber.org/zap"
)

const (
	// durablePrefix nombra los consumidores durables; uno por subject porque el
	// consumidor de JetStream filtra por un unico subject.
	durablePrefix = "suppression-ingest"
	// resolveTimeout acota resolver la base de la empresa y escribir la exclusion.
	resolveTimeout = 15 * time.Second
	// subscribeRetry es la espera entre intentos de suscripcion. Los streams
	// TRANSACTIONAL y CONTACTS los declaran sus duenos; si este servicio arranca antes
	// que ellos, la suscripcion falla ("no stream matches subject") y se reintenta hasta
	// que existan, sin bloquear el HTTP.
	subscribeRetry = 10 * time.Second
)

// subscription es cada subject que se consume y el durable que lo sigue.
type subscription struct {
	subject string
	durable string
}

// IngestWorker mantiene las suscripciones durables y traduce cada evento en una
// operacion idempotente sobre la lista.
type IngestWorker struct {
	bus      *events.Bus
	uc       *app.UseCase
	tenantDB *db.TenantDB
	logger   *zap.Logger

	mu   sync.Mutex
	subs []*natsgo.Subscription
}

func NewIngestWorker(bus *events.Bus, uc *app.UseCase, tenantDB *db.TenantDB, logger *zap.Logger) *IngestWorker {
	return &IngestWorker{bus: bus, uc: uc, tenantDB: tenantDB, logger: logger}
}

// Start suscribe en segundo plano y reintenta cada subject que aun no tiene stream
// hasta conseguirlo o hasta que el contexto termine.
func (w *IngestWorker) Start(ctx context.Context) {
	pending := []subscription{
		{subject: app.SubjectEmailBounced, durable: durablePrefix + "-bounced"},
		{subject: app.SubjectEmailComplained, durable: durablePrefix + "-complained"},
		{subject: app.SubjectContactResubscribe, durable: durablePrefix + "-resubscribed"},
	}
	go func() {
		t := time.NewTicker(subscribeRetry)
		defer t.Stop()
		for {
			pending = w.subscribeAll(pending)
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

// subscribeAll intenta cada suscripcion pendiente y devuelve las que siguen sin stream.
func (w *IngestWorker) subscribeAll(pending []subscription) []subscription {
	var still []subscription
	for _, s := range pending {
		sub, err := w.bus.DurableQueueSubscribe(s.subject, s.durable, w.handle(s.subject))
		if err != nil {
			w.logger.Warn("suppression: sin stream para el subject; se reintentara",
				zap.String("subject", s.subject), zap.Error(err))
			still = append(still, s)
			continue
		}
		w.mu.Lock()
		w.subs = append(w.subs, sub)
		w.mu.Unlock()
		w.logger.Info("suppression: suscrito", zap.String("subject", s.subject), zap.String("durable", s.durable))
	}
	return still
}

func (w *IngestWorker) Stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, s := range w.subs {
		_ = s.Drain()
	}
	w.subs = nil
}

// handle devuelve el manejador de un subject. Sin ack, JetStream reentrega; se acka lo
// procesado y tambien lo que nunca va a poder procesarse (payload roto, empresa que no
// existe): reintentar eso solo envenena la cola.
func (w *IngestWorker) handle(subject string) func(events.Event, func()) {
	return func(evt events.Event, ack func()) {
		data, _ := evt.Data.(map[string]interface{})
		tenantID, err := uuid.Parse(str(data["tenant_id"]))
		if err != nil {
			tenantID, err = uuid.Parse(evt.TenantID)
		}
		if err != nil {
			w.logger.Error("suppression: evento sin tenant_id; se descarta", zap.String("subject", subject))
			ack()
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), resolveTimeout)
		defer cancel()
		pool, err := w.tenantDB.ResolveForTenant(ctx, tenantID.String())
		if err != nil {
			if db.IsUnknownTenant(err) {
				w.logger.Error("suppression: evento de una empresa inexistente; se descarta",
					zap.String("subject", subject), zap.String("tenant_id", tenantID.String()))
				ack()
				return
			}
			w.logger.Warn("suppression: empresa sin pool; se reintentara", zap.Error(err))
			return
		}
		ctx = db.WithTenant(ctx, pool, tenantID.String())

		if subject == app.SubjectContactResubscribe {
			at, err := consentedAt(data["consented_at"])
			if err != nil {
				w.logger.Error("suppression: resuscripcion con consented_at invalido; se descarta y la baja sigue",
					zap.String("tenant_id", tenantID.String()), zap.Error(err))
				ack()
				return
			}
			w.resubscribe(ctx, tenantID, str(data["email"]), at, ack)
			return
		}

		ev := app.DeliveryEvent{
			Subject:    subject,
			TenantID:   tenantID,
			Email:      str(data["email"]),
			BounceType: str(data["bounce_type"]),
			Detail:     str(data["detail"]),
		}
		if id, err := uuid.Parse(str(data["message_id"])); err == nil {
			ev.MessageID = &id
		}
		res, err := w.uc.Ingest(ctx, ev)
		if err != nil {
			if isPermanent(err) {
				w.logger.Error("suppression: evento invalido; se descarta",
					zap.String("subject", subject), zap.Error(err))
				ack()
				return
			}
			w.logger.Warn("suppression: no se pudo registrar la exclusion; se reintentara",
				zap.String("subject", subject), zap.Error(err))
			return
		}
		if res.Added {
			w.logger.Info("suppression: direccion excluida por evento",
				zap.String("subject", subject), zap.String("tenant_id", tenantID.String()))
		}
		ack()
	}
}

// errInvalidConsentedAt: el campo viene pero no es una hora RFC 3339. Reintentar no lo
// arregla, y levantar la baja sin saber cuando se consintio podria retirar una baja
// posterior, asi que el evento se descarta y la baja sigue.
var errInvalidConsentedAt = errors.New("consented_at no es una hora RFC 3339")

// consentedAt lee la hora del consentimiento de contacts.contact.resubscribed. Sin el
// campo (productor anterior) devuelve nil y la baja se levanta como antes.
func consentedAt(v interface{}) (*time.Time, error) {
	if v == nil {
		return nil, nil
	}
	s, ok := v.(string)
	if !ok {
		return nil, errInvalidConsentedAt
	}
	at, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return nil, errInvalidConsentedAt
	}
	return &at, nil
}

func (w *IngestWorker) resubscribe(ctx context.Context, tenantID uuid.UUID, email string, at *time.Time, ack func()) {
	removed, err := w.uc.Resubscribe(ctx, tenantID, email, at)
	if err != nil {
		if isPermanent(err) {
			w.logger.Error("suppression: resuscripcion con direccion invalida; se descarta", zap.Error(err))
			ack()
			return
		}
		w.logger.Warn("suppression: no se pudo levantar la baja; se reintentara", zap.Error(err))
		return
	}
	if removed {
		w.logger.Info("suppression: baja levantada por nuevo consentimiento", zap.String("tenant_id", tenantID.String()))
	}
	ack()
}

// isPermanent distingue el error que no cambia al reintentar (una direccion o causa
// invalida en el payload) del transitorio (la base no respondio).
func isPermanent(err error) bool {
	return app.IsInputError(err)
}

func str(v interface{}) string {
	s, _ := v.(string)
	return s
}
