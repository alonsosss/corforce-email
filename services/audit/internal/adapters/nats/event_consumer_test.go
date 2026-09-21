package nats

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/services/audit/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	natsgo "github.com/nats-io/nats.go"
	dto "github.com/prometheus/client_model/go"
	"go.uber.org/zap"
)

// journal deja el orden en que pasaron las cosas: es lo que distingue "confirmar tras guardar" de
// "guardar tras confirmar".
type journal struct {
	mu      sync.Mutex
	entries []string
}

func (j *journal) add(s string) {
	j.mu.Lock()
	j.entries = append(j.entries, s)
	j.mu.Unlock()
}

func (j *journal) list() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]string(nil), j.entries...)
}

type fakeRecorder struct {
	journal *journal
	saved   map[uuid.UUID]*domain.AuditLog
	fail    error
}

func newRecorder(j *journal) *fakeRecorder {
	return &fakeRecorder{journal: j, saved: map[uuid.UUID]*domain.AuditLog{}}
}

func (r *fakeRecorder) RecordAction(_ context.Context, l *domain.AuditLog) (bool, error) {
	if r.fail != nil {
		return false, r.fail
	}
	if l.ID == uuid.Nil {
		l.ID = uuid.New()
	}
	if _, dup := r.saved[l.ID]; dup {
		return false, nil
	}
	r.saved[l.ID] = l
	r.journal.add("guardado")
	return true, nil
}

type fakeInspector struct{ seen []string }

func (i *fakeInspector) Inspect(_ context.Context, l *domain.AuditLog, _ string) {
	i.seen = append(i.seen, l.Action)
}

type fakeTenants struct{ err error }

func (t fakeTenants) ResolveForTenant(context.Context, string) (*pgxpool.Pool, error) {
	return nil, t.err
}

type rig struct {
	consumer  *EventConsumer
	journal   *journal
	recorder  *fakeRecorder
	inspector *fakeInspector
	tenants   *fakeTenants
	acks      int
}

func newRig() *rig {
	j := &journal{}
	r := &rig{journal: j, recorder: newRecorder(j), inspector: &fakeInspector{}, tenants: &fakeTenants{}}
	r.consumer = NewEventConsumer(&fakeBus{}, r.recorder, r.inspector, r.tenants, nil, zap.NewNop())
	return r
}

func (r *rig) handle(evt events.Event) {
	r.consumer.Handle(evt, func() { r.acks++; r.journal.add("ack") })
}

func loginEvent(tenant uuid.UUID) events.Event {
	return events.Event{
		ID: uuid.NewString(), Type: "user.logged_in", Source: "identity", TenantID: tenant.String(), UserID: uuid.NewString(),
		Data: map[string]interface{}{"ip": "203.0.113.9", "user_agent": "curl/8"},
	}
}

func TestElAckLlegaDespuesDeGuardarElApunte(t *testing.T) {
	r := newRig()
	r.handle(loginEvent(uuid.New()))
	if got := strings.Join(r.journal.list(), ","); got != "guardado,ack" {
		t.Fatalf("orden %q: confirmar antes de guardar pierde el evento si la base falla", got)
	}
	if len(r.inspector.seen) != 1 || r.inspector.seen[0] != "user.logged_in" {
		t.Fatalf("el detector debe ver el apunte nuevo: %v", r.inspector.seen)
	}
}

func TestUnFalloTransitorioDejaElEventoSinConfirmar(t *testing.T) {
	r := newRig()
	r.recorder.fail = errors.New("conexion rechazada")
	r.handle(loginEvent(uuid.New()))
	if r.acks != 0 || len(r.inspector.seen) != 0 {
		t.Fatalf("acks %d, inspecciones %v: sin apunte no hay confirmacion", r.acks, r.inspector.seen)
	}

	r.recorder.fail = nil
	evt := loginEvent(uuid.New())
	r.handle(evt)
	if r.acks != 1 || len(r.recorder.saved) != 1 {
		t.Fatalf("recuperada la base: acks %d, apuntes %d", r.acks, len(r.recorder.saved))
	}
}

func TestLaReentregaNoDuplicaElApunteNiRepiteElDetector(t *testing.T) {
	r := newRig()
	evt := loginEvent(uuid.New())
	r.handle(evt)
	r.handle(evt)
	r.handle(evt)
	if len(r.recorder.saved) != 1 {
		t.Fatalf("%d apuntes de un mismo evento", len(r.recorder.saved))
	}
	if r.acks != 3 {
		t.Fatalf("acks %d: la reentrega tambien se confirma, o girara hasta agotarse", r.acks)
	}
	if len(r.inspector.seen) != 1 {
		t.Fatalf("el detector corrio %d veces por un mismo evento", len(r.inspector.seen))
	}
	id, _ := uuid.Parse(evt.ID)
	if r.recorder.saved[id] == nil {
		t.Fatal("el id del apunte es el del evento")
	}
}

func TestUnEventoSinEmpresaSeConfirmaYSeCuenta(t *testing.T) {
	for name, tenant := range map[string]string{"vacia": "", "ilegible": "no-es-uuid", "nula": uuid.Nil.String()} {
		t.Run(name, func(t *testing.T) {
			r := newRig()
			before := counter(t, reasonNoTenant)
			evt := loginEvent(uuid.New())
			evt.TenantID = tenant
			r.handle(evt)
			if r.acks != 1 || len(r.recorder.saved) != 0 {
				t.Fatalf("acks %d, apuntes %d", r.acks, len(r.recorder.saved))
			}
			if got := counter(t, reasonNoTenant) - before; got != 1 {
				t.Fatalf("contador %v", got)
			}
		})
	}
}

func TestUnaEmpresaInexistenteSeConfirmaPeroUnaBaseCaidaNo(t *testing.T) {
	r := newRig()
	r.tenants.err = pgx.ErrNoRows
	before := counter(t, reasonUnknownTenant)
	r.handle(loginEvent(uuid.New()))
	if r.acks != 1 || counter(t, reasonUnknownTenant)-before != 1 {
		t.Fatalf("empresa inexistente: acks %d", r.acks)
	}

	r = newRig()
	r.tenants.err = errors.New("registro no responde")
	r.handle(loginEvent(uuid.New()))
	if r.acks != 0 || len(r.recorder.saved) != 0 {
		t.Fatalf("registro caido: acks %d; se reintenta", r.acks)
	}
}

func TestElApunteDeUnEventoTomaSuIdYRecortaLoQueNoCabe(t *testing.T) {
	tenant, id := uuid.New(), uuid.New()
	long := strings.Repeat("x", 300)
	l, ua := eventLog(events.Event{
		ID: id.String(), Type: long, Source: long, TenantID: tenant.String(),
		Data: map[string]interface{}{"ip": long, "user_agent": "agente"},
	}, tenant)
	if l.ID != id || l.TenantID != tenant || ua != "agente" || l.UserAgent == nil || *l.UserAgent != "agente" {
		t.Fatalf("apunte %+v", l)
	}
	if len(l.Action) != maxShortColumn || len(l.Module) != maxShortColumn || len(l.IPAddress) != maxIPColumn {
		t.Fatalf("action %d, module %d, ip %d: un valor que no cabe hace fallar el INSERT en cada reentrega", len(l.Action), len(l.Module), len(l.IPAddress))
	}
	if l.Resource != long || l.Changes == nil {
		t.Fatalf("resource es text: no se recorta; changes %v", l.Changes)
	}

	sin, _ := eventLog(events.Event{Type: "t", Source: "s"}, tenant)
	if sin.IPAddress != "0.0.0.0" || sin.UserAgent != nil || sin.Changes != nil || sin.ID != uuid.Nil {
		t.Fatalf("sin datos ni id: %+v", sin)
	}
}

func TestUnIdDeEventoQueNoEsUuidDaSiempreElMismoApunte(t *testing.T) {
	a, b := eventLogID("evt-1"), eventLogID("evt-1")
	if a == uuid.Nil || a != b || a == eventLogID("evt-2") {
		t.Fatalf("ids derivados %v %v", a, b)
	}
}

func TestLosSubjectsDeAuditDanSusStreamsYConsumidores(t *testing.T) {
	sources, err := SourcesFor(strings.Split("identity.>,organization.>,access.>,gateway.>,scheduler.>,domains.>,migration.>,mail.>", ","))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][2]string{
		"identity.>":     {"IDENTITY", "audit-identity-all"},
		"organization.>": {"ORGANIZATION", "audit-organization-all"},
		"access.>":       {"ACCESS", "audit-access-all"},
		"gateway.>":      {"GATEWAY", "audit-gateway-all"},
		"scheduler.>":    {"SCHEDULER", "audit-scheduler-all"},
		"domains.>":      {"DOMAINS", "audit-domains-all"},
		"migration.>":    {"MIGRATION", "audit-migration-all"},
		"mail.>":         {"MAIL_DIRECTORY", "audit-mail-all"},
	}
	if len(sources) != len(want) {
		t.Fatalf("%d fuentes", len(sources))
	}
	for _, s := range sources {
		if got := [2]string{s.Stream, s.Durable}; got != want[s.Subject] {
			t.Errorf("%s: %v, se esperaba %v", s.Subject, got, want[s.Subject])
		}
	}
}

func TestUnSubjectSinPrimerTokenLiteralOChocanteSeRechaza(t *testing.T) {
	for _, bad := range [][]string{{">"}, {"*.user.>"}, {""}, {"$JS.API.>"}, {"a.>", "a.>", "a.all"}} {
		if _, err := SourcesFor(bad); err == nil {
			t.Errorf("%v se acepto", bad)
		}
	}
	if s, err := SourcesFor([]string{"a.>", "a.>"}); err != nil || len(s) != 1 {
		t.Fatalf("un subject repetido es uno solo: %v %v", s, err)
	}
}

func counter(t *testing.T, reason string) float64 {
	t.Helper()
	var m dto.Metric
	if err := discarded.WithLabelValues(reason).Write(&m); err != nil {
		t.Fatal(err)
	}
	return m.GetCounter().GetValue()
}

// fakeBus registra lo declarado y ata cada durable a su handler. Falla las primeras failures
// llamadas de EnsureStream, como un NATS que aun no responde.
type fakeBus struct {
	mu       sync.Mutex
	failures int
	ensured  []string
	bound    []string
}

func (b *fakeBus) EnsureStream(name string, subjects []string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failures > 0 {
		b.failures--
		return errors.New("nats no responde")
	}
	b.ensured = append(b.ensured, name+"="+strings.Join(subjects, ","))
	return nil
}

func (b *fakeBus) DurableQueueSubscribeNew(subject, durable string, _ func(events.Event, func())) (*natsgo.Subscription, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.bound = append(b.bound, durable)
	return nil, nil
}

func TestSiNatsNoRespondeElServicioReintentaYAtaTodo(t *testing.T) {
	bus := &fakeBus{failures: 3}
	sources, _ := SourcesFor([]string{"identity.>", "gateway.>"})
	c := NewEventConsumer(bus, newRecorder(&journal{}), &fakeInspector{}, &fakeTenants{}, sources, zap.NewNop())
	c.retry = time.Millisecond

	done := make(chan struct{})
	go func() { c.Run(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run no termino con NATS ya recuperado")
	}
	if len(bus.bound) != 2 {
		t.Fatalf("consumidores atados: %v", bus.bound)
	}
	seen := map[string]int{}
	for _, d := range bus.bound {
		seen[d]++
	}
	if seen["audit-identity-all"] != 1 || seen["audit-gateway-all"] != 1 {
		t.Fatalf("un durable ya atado no se ata otra vez: %v", bus.bound)
	}
	c.Stop()
}

func TestRunTerminaSiSeCancelaElContextoConNatsCaido(t *testing.T) {
	bus := &fakeBus{failures: 1 << 30}
	sources, _ := SourcesFor([]string{"identity.>"})
	c := NewEventConsumer(bus, newRecorder(&journal{}), &fakeInspector{}, &fakeTenants{}, sources, zap.NewNop())
	c.retry = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run no respeto la cancelacion")
	}
}
