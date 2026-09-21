package nats

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/services/audit/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	natsgo "github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

const (
	subscribeRetry = 5 * time.Second
	// handleTimeout acota el trabajo de audit por cada evento: la suscripcion entrega de una en una y
	// un evento colgado retiene a los que vienen detras.
	handleTimeout = 30 * time.Second

	maxIPColumn = 45
)

// eventIDNamespace deriva un id de apunte estable de un id de evento que no es un uuid.
var eventIDNamespace = uuid.MustParse("6f1c2b0e-4d5a-4e0b-9a51-3c7d8e2f9a10")

// discarded cuenta los eventos que se dan por tratados sin apunte porque ninguna reentrega los
// arreglaria. Que no quede rastro de un evento es lo que este servicio existe para evitar: por eso
// se cuenta, en vez de solo registrarlo.
var discarded = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "audit_events_discarded_total",
	Help: "Eventos del bus que audit dio por tratados sin guardarlos en el rastro: sin empresa (no hay base donde escribirlos) o de una empresa que ya no existe.",
}, []string{"reason"})

const (
	reasonNoTenant      = "no_tenant"
	reasonUnknownTenant = "unknown_tenant"
)

func init() {
	prometheus.MustRegister(discarded)
	for _, reason := range []string{reasonNoTenant, reasonUnknownTenant} {
		discarded.WithLabelValues(reason)
	}
}

// Bus es lo que el consumidor necesita del bus de eventos.
type Bus interface {
	EnsureStream(name string, subjects []string) error
	DurableQueueSubscribeNew(subject, durable string, handler func(events.Event, func())) (*natsgo.Subscription, error)
}

// Recorder guarda el apunte de un evento y dice si es nuevo (false: ya estaba guardado).
type Recorder interface {
	RecordAction(ctx context.Context, l *domain.AuditLog) (created bool, err error)
}

// Inspector evalua un apunte recien guardado en busca de conductas sospechosas.
type Inspector interface {
	Inspect(ctx context.Context, l *domain.AuditLog, userAgent string)
}

// TenantPools resuelve la base de una empresa.
type TenantPools interface {
	ResolveForTenant(ctx context.Context, tenantID string) (*pgxpool.Pool, error)
}

// Source es un flujo del bus que audit vuelca en el rastro: el subject a escuchar, el stream de
// JetStream que lo retiene y el nombre del consumidor durable.
type Source struct {
	Stream  string
	Subject string
	Durable string
}

// streamOverrides son los streams cuyo nombre no es el primer token del subject en mayusculas.
var streamOverrides = map[string]string{"mail": "MAIL_DIRECTORY"}

// SourcesFor deriva de los subjects configurados (AUDIT_SUBJECTS) los streams y los consumidores.
// El stream es el que su dueno ya declara (identity.> en IDENTITY, migration.> en MIGRATION, ...) o,
// donde no lo hay (gateway.>, access.>), uno propio con el primer token en mayusculas: EnsureStream
// une los subjects y nunca quita los ajenos. Un subject cuyo primer token no es un nombre literal no
// admite stream, y dos subjects que dan el mismo durable se rechazan.
func SourcesFor(subjects []string) ([]Source, error) {
	seen := make(map[string]string, len(subjects))
	sources := make([]Source, 0, len(subjects))
	for _, subject := range subjects {
		first, _, _ := strings.Cut(subject, ".")
		if first == "" || strings.ContainsAny(first, "*>$ ") {
			return nil, fmt.Errorf("subject %q: el primer token debe ser literal", subject)
		}
		stream, ok := streamOverrides[first]
		if !ok {
			stream = strings.ToUpper(strings.NewReplacer("-", "_").Replace(first))
		}
		durable := "audit-" + strings.NewReplacer(".", "-", ">", "all", "*", "any").Replace(subject)
		if other, dup := seen[durable]; dup {
			if other == subject {
				continue
			}
			return nil, fmt.Errorf("subjects %q y %q dan el mismo consumidor %q", other, subject, durable)
		}
		seen[durable] = subject
		sources = append(sources, Source{Stream: stream, Subject: subject, Durable: durable})
	}
	return sources, nil
}

// EventConsumer vuelca en el rastro de cada empresa los eventos de dominio del bus por consumidores
// durables de JetStream: lo publicado mientras audit esta caido (cada despliegue lo recrea) queda en
// el stream y se aplica al volver. El ack llega solo tras guardar el apunte, y el id del evento es el
// id del apunte: una reentrega (el apunte se guardo pero el ack se perdio) choca con el ya guardado
// en vez de duplicarlo. Un evento que no se puede guardar se deja sin ack: JetStream lo reentrega y,
// agotadas sus entregas, pasa a EVENTS_DLQ, que alerta; los que vienen detras no esperan.
type EventConsumer struct {
	bus       Bus
	recorder  Recorder
	inspector Inspector
	tenants   TenantPools
	logger    *zap.Logger
	sources   []Source
	retry     time.Duration

	mu   sync.Mutex
	subs []*natsgo.Subscription
}

func NewEventConsumer(bus Bus, recorder Recorder, inspector Inspector, tenants TenantPools, sources []Source, logger *zap.Logger) *EventConsumer {
	return &EventConsumer{bus: bus, recorder: recorder, inspector: inspector, tenants: tenants, sources: sources, logger: logger, retry: subscribeRetry}
}

// Run declara los streams y ata los consumidores. Bloquea hasta lograrlo con todos o hasta que el
// contexto se cancele: si NATS no responde reintenta, y mientras tanto el servicio ya atiende su API.
// Lo publicado hasta entonces lo recoge el durable al atarse.
func (c *EventConsumer) Run(ctx context.Context) {
	pending := c.sources
	for len(pending) > 0 {
		var failed []Source
		for _, src := range pending {
			if err := c.subscribe(src); err != nil {
				c.logger.Warn("audit: no se pudo suscribir a los eventos; se reintenta",
					zap.String("subject", src.Subject), zap.String("consumer", src.Durable), zap.Error(err))
				failed = append(failed, src)
			}
		}
		if pending = failed; len(pending) == 0 {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(c.retry):
		}
	}
}

func (c *EventConsumer) subscribe(src Source) error {
	if err := c.bus.EnsureStream(src.Stream, []string{src.Subject}); err != nil {
		return fmt.Errorf("stream %s: %w", src.Stream, err)
	}
	sub, err := c.bus.DurableQueueSubscribeNew(src.Subject, src.Durable, c.Handle)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.subs = append(c.subs, sub)
	c.mu.Unlock()
	c.logger.Info("audit: suscrito a los eventos", zap.String("subject", src.Subject), zap.String("stream", src.Stream), zap.String("consumer", src.Durable))
	return nil
}

// Stop cierra las suscripciones tras terminar los mensajes en vuelo. Los consumidores durables
// siguen en el servidor con su posicion.
func (c *EventConsumer) Stop() {
	c.mu.Lock()
	subs := c.subs
	c.subs = nil
	c.mu.Unlock()
	for _, s := range subs {
		_ = s.Drain()
	}
}

// Handle guarda el apunte del evento y confirma. Sin confirmar quedan los fallos que una nueva
// entrega puede arreglar (la base de la empresa no responde); se confirman sin apunte solo los
// eventos que ninguna reentrega arreglaria, y cada uno se cuenta y se registra.
func (c *EventConsumer) Handle(evt events.Event, ack func()) {
	tenantID, err := uuid.Parse(evt.TenantID)
	if err != nil || tenantID == uuid.Nil {
		c.discard(reasonNoTenant, evt, ack)
		return
	}
	base, cancel := context.WithTimeout(context.Background(), handleTimeout)
	defer cancel()
	pool, err := c.tenants.ResolveForTenant(base, tenantID.String())
	if err != nil {
		if db.IsUnknownTenant(err) {
			c.discard(reasonUnknownTenant, evt, ack)
			return
		}
		c.logger.Warn("audit: base de la empresa no disponible; se reintentara",
			zap.String("tenant_id", tenantID.String()), zap.String("event_id", evt.ID), zap.Error(err))
		return
	}
	ctx := db.WithPool(base, pool)

	l, userAgent := eventLog(evt, tenantID)
	created, err := c.recorder.RecordAction(ctx, l)
	if err != nil {
		c.logger.Warn("audit: no se pudo guardar el apunte del evento; se reintentara",
			zap.String("type", evt.Type), zap.String("event_id", evt.ID), zap.Error(err))
		return
	}
	ack()
	if created {
		c.inspector.Inspect(ctx, l, userAgent)
	}
}

func (c *EventConsumer) discard(reason string, evt events.Event, ack func()) {
	discarded.WithLabelValues(reason).Inc()
	c.logger.Error("audit: evento sin apunte: ninguna reentrega lo arreglaria",
		zap.String("reason", reason), zap.String("type", evt.Type), zap.String("source", evt.Source), zap.String("event_id", evt.ID))
	ack()
}

// eventLog arma el apunte de un evento de dominio. Su id es el del evento (o uno derivado de el):
// con el, la reentrega del bus choca con el apunte ya guardado. La IP real y el user-agent viajan en
// el payload cuando el emisor los conoce (identity en user.logged_in); 0.0.0.0 solo es ausencia.
func eventLog(evt events.Event, tenantID uuid.UUID) (*domain.AuditLog, string) {
	userID, _ := uuid.Parse(evt.UserID)
	ip, userAgent, detail := "0.0.0.0", "", ""
	if data, ok := evt.Data.(map[string]interface{}); ok {
		if b, err := json.Marshal(data); err == nil {
			detail = string(b)
		}
		if v, ok := data["ip"].(string); ok && v != "" {
			ip = truncateRunes(v, maxIPColumn)
		}
		if v, ok := data["user_agent"].(string); ok {
			userAgent = v
		}
	}
	l := &domain.AuditLog{
		ID:        eventLogID(evt.ID),
		TenantID:  tenantID,
		UserID:    userID,
		Action:    truncateRunes(evt.Type, maxShortColumn),
		Module:    truncateRunes(evt.Source, maxShortColumn),
		Resource:  evt.Type,
		IPAddress: ip,
		Severity:  "info",
	}
	if userAgent != "" {
		l.UserAgent = &userAgent
	}
	if detail != "" {
		l.Changes = &detail
	}
	return l, userAgent
}

// eventLogID es el id del apunte de un evento: el del evento si es un uuid, uno derivado de su texto
// si no, y uuid.Nil (que la bitacora reemplaza por uno nuevo) si el evento no trae id.
func eventLogID(eventID string) uuid.UUID {
	if eventID == "" {
		return uuid.Nil
	}
	if id, err := uuid.Parse(eventID); err == nil {
		return id
	}
	return uuid.NewSHA1(eventIDNamespace, []byte(eventID))
}
