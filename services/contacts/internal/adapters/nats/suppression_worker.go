// Package nats consume los hechos de suppression que cambian el estado de un contacto:
// el alta y la retirada de cada causa de exclusion.
package nats

import (
	"context"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/services/contacts/internal/app"
	"github.com/google/uuid"
	natsgo "github.com/nats-io/nats.go"
	"go.uber.org/zap"
)

const (
	// resolveTimeout acota resolver la base de la empresa y aplicar el cambio.
	resolveTimeout = 15 * time.Second
	// subscribeRetry: el stream SUPPRESSION lo declara su dueno; si este servicio
	// arranca antes, la suscripcion falla y se reintenta sin bloquear el HTTP.
	subscribeRetry = 10 * time.Second
	// legacyWarnEvery acota el aviso de eventos sin reasons: en un despliegue escalonado
	// llegan en rafaga y basta con saber que siguen llegando y cuantos.
	legacyWarnEvery = 10 * time.Minute
)

// hasReasons indica si el evento trae la lista de causas vigentes, aunque este vacia.
func hasReasons(data map[string]interface{}) bool {
	_, ok := data["reasons"].([]interface{})
	return ok
}

// throttle deja pasar un aviso por intervalo y cuenta los que calla entre medias.
type throttle struct {
	every time.Duration

	mu      sync.Mutex
	last    time.Time
	skipped int
}

// allow devuelve si toca avisar y cuantos avisos se callaron desde el anterior.
func (t *throttle) allow(now time.Time) (bool, int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.last.IsZero() && now.Sub(t.last) < t.every {
		t.skipped++
		return false, 0
	}
	skipped := t.skipped
	t.last, t.skipped = now, 0
	return true, skipped
}

type subscription struct {
	subject string
	durable string
}

// subscriptions: un durable por subject, porque el consumidor de JetStream filtra por
// un unico subject.
var subscriptions = []subscription{
	{subject: app.SubjectSuppressionAdded, durable: "contacts-suppression"},
	{subject: app.SubjectSuppressionRemoved, durable: "contacts-suppression-removed"},
}

type SuppressionWorker struct {
	bus      *events.Bus
	uc       *app.UseCase
	tenantDB *db.TenantDB
	logger   *zap.Logger

	legacy throttle

	mu   sync.Mutex
	subs []*natsgo.Subscription
}

func NewSuppressionWorker(bus *events.Bus, uc *app.UseCase, tenantDB *db.TenantDB, logger *zap.Logger) *SuppressionWorker {
	return &SuppressionWorker{bus: bus, uc: uc, tenantDB: tenantDB, logger: logger, legacy: throttle{every: legacyWarnEvery}}
}

// Start suscribe en segundo plano y reintenta lo que aun no tiene stream.
func (w *SuppressionWorker) Start(ctx context.Context) {
	pending := append([]subscription(nil), subscriptions...)
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

func (w *SuppressionWorker) subscribeAll(pending []subscription) []subscription {
	var still []subscription
	for _, s := range pending {
		sub, err := w.bus.DurableQueueSubscribe(s.subject, s.durable, w.handle(s.subject))
		if err != nil {
			w.logger.Warn("contacts: sin stream para el subject; se reintentara",
				zap.String("subject", s.subject), zap.Error(err))
			still = append(still, s)
			continue
		}
		w.mu.Lock()
		w.subs = append(w.subs, sub)
		w.mu.Unlock()
		w.logger.Info("contacts: suscrito", zap.String("subject", s.subject), zap.String("durable", s.durable))
	}
	return still
}

func (w *SuppressionWorker) Stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, s := range w.subs {
		_ = s.Drain()
	}
	w.subs = nil
}

// handle: sin ack, JetStream reentrega. Se acka lo procesado y lo que nunca se va a
// poder procesar (payload roto, empresa inexistente, direccion invalida); un fallo de la
// base o de la consulta a suppression se deja sin ack para reintentar.
func (w *SuppressionWorker) handle(subject string) func(events.Event, func()) {
	return func(evt events.Event, ack func()) {
		data, _ := evt.Data.(map[string]interface{})
		tenantID, err := uuid.Parse(str(data["tenant_id"]))
		if err != nil {
			tenantID, err = uuid.Parse(evt.TenantID)
		}
		if err != nil {
			w.logger.Error("contacts: evento sin tenant_id; se descarta", zap.String("subject", subject))
			ack()
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), resolveTimeout)
		defer cancel()
		pool, err := w.tenantDB.ResolveForTenant(ctx, tenantID.String())
		if err != nil {
			if db.IsUnknownTenant(err) {
				w.logger.Error("contacts: evento de una empresa inexistente; se descarta",
					zap.String("subject", subject), zap.String("tenant_id", tenantID.String()))
				ack()
				return
			}
			w.logger.Warn("contacts: empresa sin pool; se reintentara", zap.Error(err))
			return
		}
		ctx = db.WithTenant(ctx, pool, tenantID.String())

		withReasons := hasReasons(data)
		if !withReasons {
			if ok, skipped := w.legacy.allow(time.Now()); ok {
				w.logger.Warn("contacts: evento de suppression sin reasons (productor anterior); se aplica por su causa",
					zap.String("subject", subject), zap.Int("avisos_omitidos", skipped))
			}
		}
		res, err := w.uc.ApplySuppression(ctx, app.SuppressionEvent{
			Subject:    subject,
			TenantID:   tenantID,
			Email:      str(data["email"]),
			Reason:     str(data["reason"]),
			Source:     str(data["source"]),
			HasReasons: withReasons,
		})
		if err != nil {
			if app.IsInputError(err) {
				w.logger.Error("contacts: evento de suppression invalido; se descarta",
					zap.String("subject", subject), zap.Error(err))
				ack()
				return
			}
			w.logger.Warn("contacts: no se pudo aplicar la exclusion; se reintentara",
				zap.String("subject", subject), zap.Error(err))
			return
		}
		if res.Changed {
			w.logger.Info("contacts: estado del contacto actualizado por suppression",
				zap.String("subject", subject), zap.String("tenant_id", tenantID.String()))
		}
		ack()
	}
}

func str(v interface{}) string {
	s, _ := v.(string)
	return s
}
