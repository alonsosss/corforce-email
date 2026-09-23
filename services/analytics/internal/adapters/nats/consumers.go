// Package nats consume los hechos que alimentan los agregados: los hitos de cada mensaje
// que publica transactional y los cambios de estado que publica campaigns. Un consumidor
// durable por stream, filtrado por comodin: la accion viaja en el tipo del evento.
//
// Los streams los declaran sus duenos (TRANSACTIONAL, CAMPAIGNS). Si uno aun no existe,
// la suscripcion se reintenta cada subscribeRetry sin bloquear el HTTP.
package nats

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/services/analytics/internal/app"
	"github.com/alonsosss/corforce-email/services/analytics/internal/domain"
	"github.com/alonsosss/corforce-email/services/analytics/internal/ports"
	"github.com/google/uuid"
	natsgo "github.com/nats-io/nats.go"
	"go.uber.org/zap"
)

const (
	durableTransactional = "analytics-transactional"
	durableCampaigns     = "analytics-campaigns"
	// Los dos durables del scheduler: un trabajo programado, que se cierra por HTTP, y una
	// tarea puntual, que no tiene ejecucion que cerrar.
	durableSchedulerJobs  = "analytics-scheduler-jobs"
	durableSchedulerTasks = "analytics-scheduler-tasks"

	// handlerRetentionPrune es el manejador que este servicio declara en el catalogo del
	// scheduler (services/scheduler/handlers.json). Los demas manejadores del catalogo viajan
	// por el mismo subject y este consumidor los ignora.
	handlerRetentionPrune = "analytics.retention.prune"

	// processTimeout acota resolver la base de la empresa y la transaccion de un evento;
	// queda por debajo del AckWait del bus.
	processTimeout = 15 * time.Second
	// pruneTimeout acota una poda entera. Queda por debajo del AckWait del bus (90 s) para
	// que el mensaje no se reentregue mientras la poda sigue corriendo; lo que no le da
	// tiempo se borra en la siguiente, porque el DELETE va por lotes.
	pruneTimeout   = 60 * time.Second
	subscribeRetry = 10 * time.Second
)

type binding struct {
	subject string
	bind    func() (*natsgo.Subscription, error)
}

// Consumers mantiene las suscripciones durables y traduce cada evento a la ingesta.
type Consumers struct {
	bus       *events.Bus
	uc        *app.UseCase
	tenantDB  *db.TenantDB
	scheduler ports.SchedulerReporter
	logger    *zap.Logger

	mu      sync.Mutex
	subs    []*natsgo.Subscription
	stopped bool
}

func NewConsumers(bus *events.Bus, uc *app.UseCase, tenantDB *db.TenantDB, scheduler ports.SchedulerReporter, logger *zap.Logger) *Consumers {
	return &Consumers{bus: bus, uc: uc, tenantDB: tenantDB, scheduler: scheduler, logger: logger}
}

func (c *Consumers) bindings() []binding {
	return []binding{
		{subject: "transactional.email.*", bind: func() (*natsgo.Subscription, error) {
			return c.bus.DurableQueueSubscribe("transactional.email.*", durableTransactional, c.onEmailEvent)
		}},
		{subject: "campaigns.campaign.*", bind: func() (*natsgo.Subscription, error) {
			return c.bus.DurableQueueSubscribe("campaigns.campaign.*", durableCampaigns, c.onCampaignEvent)
		}},
		{subject: "scheduler.job.started", bind: func() (*natsgo.Subscription, error) {
			return c.bus.DurableQueueSubscribe("scheduler.job.started", durableSchedulerJobs, c.onSchedulerJob)
		}},
		{subject: "scheduler.task.started", bind: func() (*natsgo.Subscription, error) {
			return c.bus.DurableQueueSubscribe("scheduler.task.started", durableSchedulerTasks, c.onSchedulerTask)
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
// {tenant_id, message_id, email | to[], class?, campaign_id?, contact_id?, bounce_type?,
// link?, occurred_at?, test?}. De la direccion solo se conserva el dominio, y del enlace de
// un clic la URL normalizada sin los identificadores del destinatario. Un envio de prueba (test: true) se
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
	messageID := str(data["message_id"])
	var link string
	if milestone == domain.MilestoneClicked {
		link = domain.NormalizeLink(str(data["link"]), email, str(data["contact_id"]), messageID)
	}
	ev := domain.MessageEvent{
		EventID:         parseUUID(evt.ID),
		TenantID:        tenantID,
		MessageID:       parseUUID(messageID),
		Milestone:       milestone,
		Class:           class,
		CampaignID:      campaignID,
		RecipientDomain: domain.RecipientDomain(email),
		BounceKind:      domain.BounceKindFromProvider(str(data["bounce_type"])),
		Link:            link,
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

// Motivos por los que un despacho del scheduler no se ejecuta aqui.
var (
	// errNotMine: el manejador es de otro ejecutor del catalogo. Viaja por el mismo subject.
	errNotMine = errors.New("el manejador es de otro servicio")
	// errMalformed: el despacho no trae lo que hace falta para ejecutarlo o cerrarlo.
	errMalformed = errors.New("despacho del scheduler ilegible")
)

// schedulerJob es el despacho de un trabajo programado dirigido a este servicio.
type schedulerJob struct {
	tenantID    uuid.UUID
	executionID uuid.UUID
}

// parseSchedulerJob lee scheduler.job.started. Un trabajo de plataforma lleva data.tenant_id
// nulo: la empresa es entonces la de la base en la que vive la ejecucion, que es la del
// sobre y la que hay que devolver al cerrarla.
func parseSchedulerJob(evt events.Event) (schedulerJob, error) {
	data, _ := evt.Data.(map[string]interface{})
	if str(data["handler"]) != handlerRetentionPrune {
		return schedulerJob{}, errNotMine
	}
	executionID, err := uuid.Parse(str(data["execution_id"]))
	if err != nil {
		return schedulerJob{}, errMalformed
	}
	tenantID, err := eventTenant(evt, str(data["tenant_id"]))
	if err != nil {
		return schedulerJob{}, err
	}
	return schedulerJob{tenantID: tenantID, executionID: executionID}, nil
}

// parseSchedulerTask lee scheduler.task.started, que no lleva ejecucion que cerrar.
func parseSchedulerTask(evt events.Event) (uuid.UUID, error) {
	data, _ := evt.Data.(map[string]interface{})
	if str(data["handler"]) != handlerRetentionPrune {
		return uuid.Nil, errNotMine
	}
	return eventTenant(evt, str(data["tenant_id"]))
}

// eventTenant toma la empresa del payload y, si no viene, la del sobre.
func eventTenant(evt events.Event, raw string) (uuid.UUID, error) {
	id, err := uuid.Parse(raw)
	if err != nil {
		id, err = uuid.Parse(evt.TenantID)
	}
	if err != nil || id == uuid.Nil {
		return uuid.Nil, errMalformed
	}
	return id, nil
}

// pruneResult es lo que el scheduler guarda como resultado de la ejecucion.
type pruneResult struct {
	Messages   int64 `json:"messages"`
	Events     int64 `json:"events"`
	LinkClicks int64 `json:"link_clicks"`
}

// onSchedulerJob ejecuta la poda que el scheduler despacho y la cierra por su API interna.
// El fallo lo reintenta el SCHEDULER, con su espera creciente y su presupuesto
// (max_retries), no el bus: dos reintentos superpuestos repetirian la misma poda a
// destiempo. Por eso un fallo de la poda tambien se informa y se confirma el mensaje.
func (c *Consumers) onSchedulerJob(evt events.Event, ack func()) {
	job, err := parseSchedulerJob(evt)
	if err != nil {
		c.skip(evt, err)
		ack()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), pruneTimeout)
	defer cancel()
	res, perr := c.prune(ctx, job.tenantID)
	if perr != nil {
		if db.IsUnknownTenant(perr) {
			c.logger.Error("analytics: poda de una empresa inexistente; se descarta",
				zap.String("event_id", evt.ID), zap.String("tenant_id", job.tenantID.String()))
			ack()
			return
		}
		c.close(evt, ack, c.scheduler.Fail(ctx, job.tenantID, job.executionID, perr.Error(), true))
		return
	}
	c.close(evt, ack, c.scheduler.Complete(ctx, job.tenantID, job.executionID,
		pruneResult{Messages: res.Messages, Events: res.Events, LinkClicks: res.LinkClicks}))
}

// onSchedulerTask ejecuta una tarea puntual que el scheduler despacho. No hay ejecucion que
// cerrar: su garantia es la del bus (reentrega y, agotada, la DLQ de pkg/events). Un trabajo
// programado, que si informa como acabo, es lo que conviene a lo que deba quedar registrado.
func (c *Consumers) onSchedulerTask(evt events.Event, ack func()) {
	tenantID, err := parseSchedulerTask(evt)
	if err != nil {
		c.skip(evt, err)
		ack()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), pruneTimeout)
	defer cancel()
	res, perr := c.prune(ctx, tenantID)
	if perr != nil {
		if db.IsUnknownTenant(perr) {
			c.logger.Error("analytics: tarea de una empresa inexistente; se descarta",
				zap.String("event_id", evt.ID), zap.String("tenant_id", tenantID.String()))
			ack()
			return
		}
		c.logger.Warn("analytics: la tarea de poda no se pudo ejecutar; se reintentara",
			zap.String("event_id", evt.ID), zap.Error(perr))
		return
	}
	c.logger.Info("analytics: tarea de poda ejecutada", zap.String("tenant_id", tenantID.String()),
		zap.Int64("messages", res.Messages), zap.Int64("events", res.Events), zap.Int64("link_clicks", res.LinkClicks))
	ack()
}

// prune resuelve la base de la empresa y poda.
func (c *Consumers) prune(ctx context.Context, tenantID uuid.UUID) (app.PruneResult, error) {
	pool, err := c.tenantDB.ResolveForTenant(ctx, tenantID.String())
	if err != nil {
		return app.PruneResult{}, err
	}
	return c.uc.Prune(db.WithTenant(ctx, pool, tenantID.String()), tenantID)
}

// skip registra por que no se ejecuta un despacho. El de otro manejador no es un problema:
// todos los ejecutores del catalogo reciben el mismo subject.
func (c *Consumers) skip(evt events.Event, err error) {
	if errors.Is(err, errNotMine) {
		return
	}
	c.logger.Error("analytics: despacho del scheduler ilegible; se descarta",
		zap.String("type", evt.Type), zap.String("event_id", evt.ID), zap.Error(err))
}

// close confirma el mensaje solo cuando el scheduler registro el cierre, o cuando lo rechazo
// de forma definitiva (la ejecucion ya vencio por plazo o se cancelo). Mientras el scheduler
// no responda, el mensaje se reentrega y el cierre se vuelve a intentar.
func (c *Consumers) close(evt events.Event, ack func(), err error) {
	switch {
	case err == nil:
		ack()
	case errors.Is(err, ports.ErrReportRejected):
		c.logger.Warn("analytics: el scheduler rechazo el cierre; no se reintenta",
			zap.String("event_id", evt.ID), zap.Error(err))
		ack()
	default:
		c.logger.Warn("analytics: no se pudo cerrar la ejecucion; se reintentara",
			zap.String("event_id", evt.ID), zap.Error(err))
	}
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
