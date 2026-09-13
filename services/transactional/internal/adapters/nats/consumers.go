// Package nats contiene los consumidores durables del servicio: el worker de envio
// (transactional.message.queued) y la proyeccion de dominios (domains.domain.*).
package nats

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/services/transactional/internal/app"
	"github.com/google/uuid"
	natsgo "github.com/nats-io/nats.go"
	"go.uber.org/zap"
)

// Streams y consumidores durables. Los subjects se escriben literales en las llamadas
// por convencion del repositorio (los guardianes de streams y de eventos los leen ahi).
const (
	StreamTransactional = "TRANSACTIONAL"
	StreamDomains       = "DOMAINS"

	senderDurable  = "transactional-sender"
	domainsDurable = "transactional-domains"

	// handlerTimeout acota cada entrega; debe quedar por debajo del AckWait del bus.
	handlerTimeout = 60 * time.Second
)

// EnsureStreams declara los streams que este servicio publica y consume. Idempotente.
func EnsureStreams(bus *events.Bus) error {
	if err := bus.EnsureStream("TRANSACTIONAL", []string{"transactional.>"}); err != nil {
		return err
	}
	return bus.EnsureStream("DOMAINS", []string{"domains.>"})
}

type job struct {
	evt events.Event
	ack func()
}

// SenderWorker consume la cola de envio con N goroutines y un limitador de tasa
// compartido. La suscripcion push entrega en serie; cada entrega se pasa a un worker y
// el ack llega desde el (el bus lo admite desde otra goroutine).
type SenderWorker struct {
	bus      *events.Bus
	uc       *app.UseCase
	tenantDB *db.TenantDB
	logger   *zap.Logger
	workers  int

	jobs chan job
	wg   sync.WaitGroup
	sub  *natsgo.Subscription
	stop chan struct{}
}

func NewSenderWorker(bus *events.Bus, uc *app.UseCase, tenantDB *db.TenantDB, workers int, logger *zap.Logger) *SenderWorker {
	if workers < 1 {
		workers = 1
	}
	return &SenderWorker{bus: bus, uc: uc, tenantDB: tenantDB, logger: logger, workers: workers,
		jobs: make(chan job), stop: make(chan struct{})}
}

func (w *SenderWorker) Start() error {
	for i := 0; i < w.workers; i++ {
		w.wg.Add(1)
		go w.loop()
	}
	sub, err := w.bus.DurableQueueSubscribe("transactional.message.queued", senderDurable, w.dispatch)
	if err != nil {
		close(w.stop)
		w.wg.Wait()
		return err
	}
	w.sub = sub
	return nil
}

func (w *SenderWorker) Stop() {
	if w.sub != nil {
		_ = w.sub.Drain()
	}
	close(w.stop)
	w.wg.Wait()
}

func (w *SenderWorker) dispatch(evt events.Event, ack func()) {
	select {
	case w.jobs <- job{evt: evt, ack: ack}:
	case <-w.stop:
		// Sin ack: la cola lo reentregara a la siguiente instancia.
	}
}

func (w *SenderWorker) loop() {
	defer w.wg.Done()
	for {
		select {
		case j := <-w.jobs:
			w.handle(j)
		case <-w.stop:
			return
		}
	}
}

func (w *SenderWorker) handle(j job) {
	data, _ := j.evt.Data.(map[string]interface{})
	tenantID, err1 := uuid.Parse(str(data["tenant_id"]))
	messageID, err2 := uuid.Parse(str(data["message_id"]))
	if err1 != nil || err2 != nil {
		w.logger.Error("transactional.message.queued malformado; se descarta", zap.Any("data", data))
		j.ack()
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), handlerTimeout)
	defer cancel()
	pool, err := w.tenantDB.ResolveForTenant(ctx, tenantID.String())
	if err != nil {
		if db.IsUnknownTenant(err) {
			w.logger.Error("transactional.message.queued de una empresa inexistente; se descarta",
				zap.String("tenant_id", tenantID.String()))
			j.ack()
			return
		}
		w.logger.Warn("transactional: empresa sin pool; se reintentara", zap.Error(err))
		return
	}
	ctx = db.WithTenant(ctx, pool, tenantID.String())

	outcome, err := w.uc.SendQueued(ctx, tenantID, messageID)
	if err != nil {
		w.logger.Warn("transactional: envio no completado; se reintentara",
			zap.String("message_id", messageID.String()), zap.Error(err))
		return
	}
	if outcome.Ack {
		j.ack()
	}
}

// DomainsConsumer mantiene la proyeccion transactional.sending_domains a partir de los
// eventos de domain-service.
type DomainsConsumer struct {
	bus      *events.Bus
	uc       *app.UseCase
	tenantDB *db.TenantDB
	logger   *zap.Logger
	sub      *natsgo.Subscription
}

func NewDomainsConsumer(bus *events.Bus, uc *app.UseCase, tenantDB *db.TenantDB, logger *zap.Logger) *DomainsConsumer {
	return &DomainsConsumer{bus: bus, uc: uc, tenantDB: tenantDB, logger: logger}
}

func (c *DomainsConsumer) Start() error {
	sub, err := c.bus.DurableQueueSubscribe("domains.domain.*", domainsDurable, c.handle)
	if err != nil {
		return err
	}
	c.sub = sub
	return nil
}

func (c *DomainsConsumer) Stop() {
	if c.sub != nil {
		_ = c.sub.Drain()
	}
}

func (c *DomainsConsumer) handle(evt events.Event, ack func()) {
	action := evt.Type[strings.LastIndex(evt.Type, ".")+1:]
	switch action {
	case "verified", "failed", "deleted":
	default:
		ack()
		return
	}
	data, _ := evt.Data.(map[string]interface{})
	tenantID, err := uuid.Parse(str(data["tenant_id"]))
	if err != nil {
		c.logger.Error("domains.domain.* malformado; se descarta", zap.String("type", evt.Type), zap.Any("data", data))
		ack()
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := c.tenantDB.ResolveForTenant(ctx, tenantID.String())
	if err != nil {
		if db.IsUnknownTenant(err) {
			c.logger.Error("domains.domain.* de una empresa inexistente; se descarta", zap.String("tenant_id", tenantID.String()))
			ack()
			return
		}
		c.logger.Warn("transactional: empresa sin pool; se reintentara", zap.Error(err))
		return
	}
	ctx = db.WithTenant(ctx, pool, tenantID.String())

	err = c.uc.ApplyDomainEvent(ctx, app.DomainEvent{
		Action:   action,
		TenantID: tenantID,
		Domain:   str(data["domain"]),
		Purpose:  str(data["purpose"]),
		Status:   str(data["status"]),
	})
	if err != nil {
		c.logger.Warn("transactional: proyeccion de dominio no aplicada; se reintentara",
			zap.String("type", evt.Type), zap.Error(err))
		return
	}
	ack()
}

func str(v interface{}) string {
	s, _ := v.(string)
	return s
}
