package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Registro de entregas del correo corporativo (docs/Plan_Registro_de_Envios.md, seccion 2).

const (
	// deliveryContextTTL cubre la vida maxima de un mensaje en la cola de Postfix (5 dias) con
	// margen: un aplazado que se entrega al cuarto dia sigue sabiendo quien lo envio.
	deliveryContextTTL = 7 * 24 * time.Hour
	deliveryWait       = 5 * time.Second
	deliveryMaxBackoff = time.Minute
	// deliveryTenantTTL: cuanto se recuerda la empresa de un dominio o un buzon. Un dominio que
	// cambia de empresa es excepcional; lo que llega tras el alta de uno nuevo se resuelve al
	// caducar.
	deliveryTenantTTL   = 5 * time.Minute
	deliveryPruneBatch  = 5000
	deliveryLeaderTerm  = time.Minute
	deliveryLeaderRetry = 30 * time.Second
	// deliveryBacklogEvery es cada cuanto se anota el retraso (la alerta mira 15 minutos).
	deliveryBacklogEvery = 30 * time.Second
)

// errDeliveryStore separa un fallo al guardar (reintentable) de una linea que no se puede usar.
var errDeliveryStore = errors.New("registro de entregas: fallo al guardar")

type tenantCacheEntry struct {
	tenant  uuid.UUID
	found   bool
	expires time.Time
}

// DeliveryLog lee las lineas de Postfix, las convierte en eventos por empresa y las consulta.
type DeliveryLog struct {
	source    ports.DeliveryLogSource
	repo      ports.DeliveryLogRepository
	tx        ports.Transactor
	directory ports.DirectoryReader
	metrics   ports.DeliveryLogMetrics
	retention time.Duration
	logger    *zap.Logger
	now       func() time.Time

	// backlogAt es cuando se anoto por ultima vez el retraso de la lista.
	backlogAt time.Time

	mu      sync.Mutex
	domains map[string]tenantCacheEntry
	boxes   map[string]tenantCacheEntry
}

type DeliveryLogDeps struct {
	Source ports.DeliveryLogSource
	Repo   ports.DeliveryLogRepository
	// Tx pone la consulta de una empresa bajo RLS, ademas del filtro por tenant_id.
	Tx        ports.Transactor
	Directory ports.DirectoryReader
	Metrics   ports.DeliveryLogMetrics
	Retention time.Duration
	Logger    *zap.Logger
	Now       func() time.Time
}

func NewDeliveryLog(d DeliveryLogDeps) *DeliveryLog {
	now := d.Now
	if now == nil {
		now = time.Now
	}
	logger := d.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	return &DeliveryLog{
		source: d.Source, repo: d.Repo, tx: d.Tx, directory: d.Directory, metrics: d.Metrics, retention: d.Retention,
		logger: logger, now: now, domains: map[string]tenantCacheEntry{}, boxes: map[string]tenantCacheEntry{},
	}
}

// Run lee hasta que el contexto se cancele, solo en la replica que tiene el cerrojo de lider: dos
// lectores se repartirian la lista de trabajo y desordenarian las lineas de un mismo mensaje. El
// cerrojo se renueva cada deliveryLeaderTerm para que otra replica tome el relevo si esta pierde su
// conexion a la base.
func (d *DeliveryLog) Run(ctx context.Context, acquire func(context.Context) (release func(), ok bool)) {
	for ctx.Err() == nil {
		release, ok := acquire(ctx)
		if !ok {
			if !sleepCtx(ctx, deliveryLeaderRetry) {
				return
			}
			continue
		}
		tctx, cancel := context.WithTimeout(ctx, deliveryLeaderTerm)
		d.consume(tctx)
		cancel()
		release()
	}
}

// consume lee hasta que el contexto termine. Primero acaba lo que quedo en la lista de trabajo
// (una caida a mitad); un fallo al guardar deja la linea alli y espera cada vez mas.
func (d *DeliveryLog) consume(ctx context.Context) {
	failures := 0
	for ctx.Err() == nil {
		if err := d.step(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			failures++
			wait := min(time.Second<<min(failures, 6), deliveryMaxBackoff)
			d.logger.Warn("registro de entregas: reintento", zap.Duration("espera", wait), zap.Error(err))
			if !sleepCtx(ctx, wait) {
				return
			}
			continue
		}
		failures = 0
	}
}

// step procesa lo pendiente o, sin nada pendiente, la siguiente linea.
func (d *DeliveryLog) step(ctx context.Context) error {
	d.observeBacklog(ctx)
	pending, err := d.source.Pending(ctx)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		raw, ok, err := d.source.Next(ctx, deliveryWait)
		if err != nil || !ok {
			return err
		}
		pending = []string{raw}
	}
	for _, raw := range pending {
		if err := d.Handle(ctx, raw); err != nil {
			d.metrics.DeliveryStoreFailed()
			return err
		}
		if err := d.source.Ack(ctx, raw); err != nil {
			return err
		}
	}
	return nil
}

// observeBacklog anota cuantas lineas esperan, como mucho cada deliveryBacklogEvery: con la lista
// siempre llena no hay pausa en la que anotarlo, y una consulta por linea sobraria.
func (d *DeliveryLog) observeBacklog(ctx context.Context) {
	now := d.now()
	if now.Sub(d.backlogAt) < deliveryBacklogEvery {
		return
	}
	if backlog, err := d.source.Backlog(ctx); err == nil {
		d.metrics.DeliveryBacklog(backlog)
		d.backlogAt = now
	}
}

// Handle procesa una linea. Solo devuelve error cuando conviene reintentarla (Redis o la base no
// respondieron); una linea que no sirve se cuenta y se da por procesada.
func (d *DeliveryLog) Handle(ctx context.Context, raw string) error {
	line, err := domain.ParseMailLogLine(raw)
	if err != nil {
		d.metrics.DeliveryLineProcessed("malformed")
		return nil
	}
	rec, ok := domain.ClassifyLogLine(line)
	if !ok {
		d.metrics.DeliveryLineProcessed("ignored")
		return nil
	}
	switch rec.Kind {
	case domain.LogClient, domain.LogMessageID, domain.LogFrom:
		return d.remember(ctx, rec)
	case domain.LogDelivery, domain.LogReject:
		return d.record(ctx, rec, line, raw)
	}
	d.metrics.DeliveryLineProcessed("ignored")
	return nil
}

// remember anade al contexto de su id de cola lo que aporta la linea.
func (d *DeliveryLog) remember(ctx context.Context, rec domain.LogRecord) error {
	qc, err := d.source.LoadContext(ctx, rec.QID)
	if err != nil {
		return err
	}
	switch rec.Kind {
	case domain.LogClient:
		qc.ClientIP, qc.SASLUsername = rec.ClientIP, rec.SASLUsername
	case domain.LogMessageID:
		qc.MessageID = rec.MessageID
	case domain.LogFrom:
		qc.From = rec.From
	}
	if err := d.source.SaveContext(ctx, rec.QID, qc, deliveryContextTTL); err != nil {
		return err
	}
	d.metrics.DeliveryLineProcessed("context")
	return nil
}

// record guarda el evento en cada empresa implicada: la del buzon que lo envio autenticado
// (outbound) y la del dominio destinatario (inbound). El remitente del sobre no basta para
// atribuir un envio: se puede falsificar, el usuario autenticado no.
func (d *DeliveryLog) record(ctx context.Context, rec domain.LogRecord, line domain.MailLogLine, raw string) error {
	var qc domain.QueueContext
	if rec.QID != "" {
		var err error
		if qc, err = d.source.LoadContext(ctx, rec.QID); err != nil {
			return err
		}
	}
	type target struct {
		tenant    uuid.UUID
		direction string
	}
	var targets []target
	if qc.SASLUsername != "" {
		tenant, ok, err := d.mailboxTenant(ctx, qc.SASLUsername)
		if err != nil {
			return err
		}
		if ok {
			targets = append(targets, target{tenant, domain.DirectionOutbound})
		}
	}
	// Entrante: todo lo dirigido a un dominio de la empresa, entregado en un buzon de la celda o
	// reenviado por un transporte (un dominio en convivencia con su proveedor anterior), y lo
	// rechazado a la entrada. El rechazo de un envio autenticado es de quien lo envio, no del
	// destinatario.
	if rec.Kind == domain.LogDelivery || qc.SASLUsername == "" {
		tenant, ok, err := d.domainTenant(ctx, domain.AddressDomain(rec.To))
		if err != nil {
			return err
		}
		if ok {
			targets = append(targets, target{tenant, domain.DirectionInbound})
		}
	}
	if len(targets) == 0 {
		d.metrics.DeliveryLineProcessed("unowned")
		return nil
	}
	for _, t := range targets {
		ev := domain.NewDeliveryEvent(t.tenant, t.direction, rec, qc, line, raw)
		inserted, err := d.repo.Insert(ctx, &ev)
		if err != nil {
			return fmt.Errorf("%w: %v", errDeliveryStore, err)
		}
		if inserted {
			d.metrics.DeliveryLineProcessed("stored")
		} else {
			d.metrics.DeliveryLineProcessed("duplicate")
		}
	}
	return nil
}

// mailboxTenant es la empresa del buzon autenticado.
func (d *DeliveryLog) mailboxTenant(ctx context.Context, username string) (uuid.UUID, bool, error) {
	if e, ok := d.cached(d.boxes, username); ok {
		return e.tenant, e.found, nil
	}
	mb, err := d.directory.MailboxByUsername(ctx, username)
	entry := tenantCacheEntry{}
	switch {
	case errors.Is(err, domain.ErrNotFound):
	case err != nil:
		return uuid.Nil, false, err
	default:
		entry = tenantCacheEntry{tenant: mb.TenantID, found: true}
	}
	d.store(d.boxes, username, entry)
	return entry.tenant, entry.found, nil
}

// domainTenant es la empresa del dominio, tambien a traves de un dominio alias.
func (d *DeliveryLog) domainTenant(ctx context.Context, name string) (uuid.UUID, bool, error) {
	if name == "" {
		return uuid.Nil, false, nil
	}
	if e, ok := d.cached(d.domains, name); ok {
		return e.tenant, e.found, nil
	}
	lookup := name
	if target, ok, err := d.directory.AliasDomainTarget(ctx, name); err != nil {
		return uuid.Nil, false, err
	} else if ok {
		lookup = target
	}
	states, err := d.directory.DomainStates(ctx, []string{lookup})
	if err != nil {
		return uuid.Nil, false, err
	}
	entry := tenantCacheEntry{}
	if st, ok := states[lookup]; ok {
		entry = tenantCacheEntry{tenant: st.TenantID, found: true}
	}
	d.store(d.domains, name, entry)
	return entry.tenant, entry.found, nil
}

func (d *DeliveryLog) cached(m map[string]tenantCacheEntry, key string) (tenantCacheEntry, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	e, ok := m[key]
	if !ok || d.now().After(e.expires) {
		return tenantCacheEntry{}, false
	}
	return e, true
}

// maxDeliveryCache acota la memoria de las caches: con mas entradas se vacia y se reconstruye.
const maxDeliveryCache = 10000

func (d *DeliveryLog) store(m map[string]tenantCacheEntry, key string, e tenantCacheEntry) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(m) >= maxDeliveryCache {
		clear(m)
	}
	e.expires = d.now().Add(deliveryTenantTTL)
	m[key] = e
}

// List es la consulta del registro de una empresa.
func (d *DeliveryLog) List(ctx context.Context, tenantID uuid.UUID, f domain.DeliveryFilter) ([]domain.DeliveryEvent, int64, error) {
	if f.Direction != "" && !slices.Contains(domain.DeliveryDirections(), f.Direction) {
		return nil, 0, &domain.ValidationError{Msg: "direction debe ser outbound o inbound"}
	}
	if f.Status != "" && !slices.Contains(domain.DeliveryStatuses(), f.Status) {
		return nil, 0, &domain.ValidationError{Msg: "status no válido"}
	}
	if f.DateFrom != nil && f.DateTo != nil && !f.DateFrom.Before(*f.DateTo) {
		return nil, 0, &domain.ValidationError{Msg: "date_from debe ser anterior a date_to"}
	}
	f.Normalize()
	var (
		items []domain.DeliveryEvent
		total int64
	)
	err := d.tx.TransactRLS(ctx, func(ctx context.Context) error {
		var err error
		items, total, err = d.repo.List(ctx, tenantID, f)
		return err
	})
	return items, total, err
}

// RunPruner borra cada intervalo los eventos mas antiguos que la retencion, por tandas.
func (d *DeliveryLog) RunPruner(ctx context.Context, interval time.Duration) {
	for {
		before := d.now().Add(-d.retention)
		for {
			n, err := d.repo.PruneBefore(ctx, before, deliveryPruneBatch)
			if err != nil {
				d.logger.Warn("registro de entregas: poda", zap.Error(err))
				break
			}
			if n < deliveryPruneBatch {
				break
			}
		}
		if !sleepCtx(ctx, interval) {
			return
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
