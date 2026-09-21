//go:build integration

// Pruebas del consumidor durable contra un NATS con JetStream y un Postgres reales:
//
//	NATS_TEST_URL=nats://... AUDIT_TEST_DSN=postgres://... go test -tags integration ./services/audit/internal/adapters/nats/
//
// Cada prueba usa su propio dominio de subjects (y por tanto su propio stream y sus propios
// durables), asi que no se pisan entre ellas ni con lo que ya haya en el servidor. La base se migra y
// se vacia: debe ser desechable.
package nats

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/services/audit/internal/adapters/postgres"
	"github.com/alonsosss/corforce-email/services/audit/internal/app"
	"github.com/alonsosss/corforce-email/services/audit/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	natsgo "github.com/nats-io/nats.go"
	"go.uber.org/zap"
)

func integrationEnv(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		if os.Getenv("INTEGRATION_REQUIRED") == "1" {
			t.Fatalf("%s no definida con INTEGRATION_REQUIRED=1", name)
		}
		t.Skipf("%s no definida", name)
	}
	return v
}

func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "..")
}

type staticTenants struct{ pool *pgxpool.Pool }

func (s staticTenants) ResolveForTenant(context.Context, string) (*pgxpool.Pool, error) {
	return s.pool, nil
}

type noPublisher struct{}

func (noPublisher) PublishSecurityAlert(_, _, _, _, _, _ string) error { return nil }

// countingInspector cuenta los apuntes nuevos que ve el detector.
type countingInspector struct{ n int }

func (c *countingInspector) Inspect(context.Context, *domain.AuditLog, string) { c.n++ }

type stack struct {
	t       *testing.T
	url     string
	admin   *pgxpool.Pool
	svc     *pgxpool.Pool
	tenant  uuid.UUID
	logs    *postgres.AuditLogRepo
	uc      *app.AuditUseCase
	nc      *natsgo.Conn
	js      natsgo.JetStreamContext
	prefix  string
	streams []string
}

func newStack(t *testing.T) *stack {
	t.Helper()
	dsn := integrationEnv(t, "AUDIT_TEST_DSN")
	url := integrationEnv(t, "NATS_TEST_URL")

	admin, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	files := []string{filepath.Join(repoRoot(), "migrations/tenant/canonical/platform/00_outbox.sql")}
	audit, err := filepath.Glob(filepath.Join(repoRoot(), "migrations/tenant/canonical/audit/*.sql"))
	if err != nil || len(audit) == 0 {
		t.Fatalf("migraciones de audit: %v %v", audit, err)
	}
	sort.Strings(audit)
	for _, f := range append(files, audit...) {
		sql, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := admin.Exec(context.Background(), string(sql)); err != nil {
			t.Fatalf("aplicar %s: %v", f, err)
		}
	}
	if _, err := admin.Exec(context.Background(),
		`TRUNCATE audit.data_change_records, audit.audit_logs, audit.security_events, audit.chain_anchors RESTART IDENTITY`); err != nil {
		t.Fatal(err)
	}

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		_, err := c.Exec(ctx, "SET ROLE audit_service")
		return err
	}
	svc, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)

	nc, err := natsgo.Connect(url)
	if err != nil {
		t.Fatalf("conectar a NATS: %v", err)
	}
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	if err != nil {
		t.Fatal(err)
	}

	cp := &db.ContextPool{}
	logs := postgres.NewAuditLogRepo(cp, nil)
	uc := app.NewAuditUseCase(app.AuditDeps{
		Logs: logs, Security: postgres.NewSecurityEventRepo(cp, nil), Events: noPublisher{}, Logger: zap.NewNop(),
	})
	s := &stack{
		t: t, url: url, admin: admin, svc: svc, tenant: uuid.New(), logs: logs, uc: uc, nc: nc, js: js,
		prefix: "it" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12],
	}
	t.Cleanup(func() {
		for _, name := range s.streams {
			_ = js.DeleteStream(name)
		}
	})
	return s
}

func (s *stack) subject(rest string) string { return s.prefix + "." + rest }

func (s *stack) bus() *events.Bus {
	s.t.Helper()
	bus, err := events.NewBus(s.url, zap.NewNop())
	if err != nil {
		s.t.Fatal(err)
	}
	s.t.Cleanup(bus.Close)
	return bus
}

// source es el unico flujo de la prueba: <prefijo>.> en el stream <PREFIJO>.
func (s *stack) source() Source {
	sources, err := SourcesFor([]string{s.prefix + ".>"})
	if err != nil {
		s.t.Fatal(err)
	}
	s.streams = append(s.streams, sources[0].Stream)
	return sources[0]
}

func (s *stack) consumer(bus Bus, recorder Recorder, inspector Inspector) *EventConsumer {
	return NewEventConsumer(bus, recorder, inspector, staticTenants{s.svc}, []Source{s.source()}, zap.NewNop())
}

func (s *stack) event(id uuid.UUID) events.Event {
	return events.Event{
		ID: id.String(), Type: "user.logged_in", Source: "identity", TenantID: s.tenant.String(), UserID: uuid.NewString(),
		Data: map[string]interface{}{"ip": "203.0.113.9"},
	}
}

func (s *stack) recorded() (count, distinct int) {
	s.t.Helper()
	err := s.admin.QueryRow(context.Background(),
		`SELECT count(*), count(DISTINCT id) FROM audit.audit_logs WHERE tenant_id = $1`, s.tenant).Scan(&count, &distinct)
	if err != nil {
		s.t.Fatal(err)
	}
	return count, distinct
}

func (s *stack) waitRecorded(want int) {
	s.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if n, _ := s.recorded(); n >= want {
			return
		}
		if time.Now().After(deadline) {
			n, _ := s.recorded()
			s.t.Fatalf("en el rastro hay %d apuntes, se esperaban %d", n, want)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (s *stack) waitStreamMsgs(stream string, want uint64) {
	s.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		info, err := s.js.StreamInfo(stream)
		if err == nil && info.State.Msgs >= want {
			return
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("el stream %s no llego a %d mensajes (%v)", stream, want, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func run(t *testing.T, c *EventConsumer) {
	t.Helper()
	done := make(chan struct{})
	go func() { c.Run(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("el consumidor no logro atarse")
	}
}

// La prueba que justifica el cambio: lo publicado con el consumidor parado (un despliegue, un
// reinicio) queda en el stream y, al volver, en el rastro, una vez cada evento, publicado por el
// camino de nucleo que usa identity o por JetStream, y aunque uno se reentregue a mano.
func TestLoPublicadoConElConsumidorParadoQuedaEnElRastroExactamenteUnaVez(t *testing.T) {
	s := newStack(t)
	bus := s.bus()
	src := s.source()
	inspector := &countingInspector{}

	first := s.consumer(bus, s.uc, inspector)
	run(t, first)
	first.Stop()

	const n = 60
	ids := make([]uuid.UUID, n)
	for i := range ids {
		ids[i] = uuid.New()
		evt := s.event(ids[i])
		subject := s.subject("user.logged_in")
		var err error
		if i%2 == 0 {
			err = bus.Publish(subject, evt)
		} else {
			err = bus.PublishPersistent(subject, evt)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := bus.PublishPersistent(s.subject("user.logged_in"), s.event(ids[0])); err != nil {
		t.Fatal(err)
	}
	s.waitStreamMsgs(src.Stream, n)
	if count, _ := s.recorded(); count != 0 {
		t.Fatalf("con el consumidor parado ya hay %d apuntes", count)
	}

	second := s.consumer(bus, s.uc, inspector)
	run(t, second)
	defer second.Stop()
	s.waitRecorded(n)

	// Una reentrega a mano del primero y un margen para que llegue cualquier duplicado en vuelo.
	second.Handle(s.event(ids[0]), func() {})
	time.Sleep(time.Second)

	count, distinct := s.recorded()
	if count != n || distinct != n {
		t.Fatalf("apuntes %d (distintos %d), se esperaban %d exactamente una vez", count, distinct, n)
	}
	for _, id := range ids {
		var found bool
		if err := s.admin.QueryRow(context.Background(), `SELECT EXISTS (SELECT 1 FROM audit.audit_logs WHERE id = $1)`, id).Scan(&found); err != nil || !found {
			t.Fatalf("falta el apunte del evento %s (%v)", id, err)
		}
	}
	if inspector.n != n {
		t.Fatalf("el detector vio %d apuntes nuevos, se esperaban %d", inspector.n, n)
	}
	res, err := s.logs.VerifyChain(db.WithPool(context.Background(), s.svc), s.tenant, domain.VerifyOptions{})
	if err != nil || !res.OK || res.Checked != n {
		t.Fatalf("la cadena tras la recuperacion: %+v %v", res, err)
	}
}

// El durable nuevo que reemplaza a la suscripcion de nucleo no reproduce lo que el stream ya
// retenia (lo que la suscripcion anterior ya habia guardado con otro id): solo lo posterior.
func TestUnDurableNuevoNoReproduceLoQueElStreamYaRetenia(t *testing.T) {
	s := newStack(t)
	bus := s.bus()
	src := s.source()
	if err := bus.EnsureStream(src.Stream, []string{src.Subject}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := bus.PublishPersistent(s.subject("user.logged_in"), s.event(uuid.New())); err != nil {
			t.Fatal(err)
		}
	}
	s.waitStreamMsgs(src.Stream, 5)

	c := s.consumer(bus, s.uc, &countingInspector{})
	run(t, c)
	defer c.Stop()
	for i := 0; i < 3; i++ {
		if err := bus.PublishPersistent(s.subject("user.logged_in"), s.event(uuid.New())); err != nil {
			t.Fatal(err)
		}
	}
	s.waitRecorded(3)
	time.Sleep(500 * time.Millisecond)
	if count, _ := s.recorded(); count != 3 {
		t.Fatalf("apuntes %d: solo debian entrar los 3 posteriores al durable", count)
	}
}

// Un cuerpo que no es un evento va a EVENTS_DLQ y los eventos que vienen detras se guardan igual.
func TestUnMensajeVenenosoVaALaDLQYNoBloqueaALosDemas(t *testing.T) {
	s := newStack(t)
	bus := s.bus()
	src := s.source()
	c := s.consumer(bus, s.uc, &countingInspector{})
	run(t, c)
	defer c.Stop()

	subject := s.subject("user.logged_in")
	if _, err := s.js.Publish(subject, []byte("esto no es un evento")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := bus.PublishPersistent(subject, s.event(uuid.New())); err != nil {
			t.Fatal(err)
		}
	}
	s.waitRecorded(3)

	deadline := time.Now().Add(10 * time.Second)
	for {
		m, err := s.js.GetLastMsg("EVENTS_DLQ", "dlq."+subject)
		if err == nil {
			if m.Header.Get("Dlq-Consumer") != src.Durable || m.Header.Get("Dlq-Reason") != "undecodable" {
				t.Fatalf("copia en la DLQ con cabeceras %v", m.Header)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("el mensaje venenoso no llego a EVENTS_DLQ: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// failingFor rechaza el guardado de un evento concreto, como una base que falla solo con el.
type failingFor struct {
	inner Recorder
	id    uuid.UUID
}

func (f failingFor) RecordAction(ctx context.Context, l *domain.AuditLog) (bool, error) {
	if l.ID == f.id {
		return false, fmt.Errorf("base no disponible")
	}
	return f.inner.RecordAction(ctx, l)
}

// Un evento que no se puede guardar queda sin confirmar (JetStream lo reentrega, y agotadas sus
// entregas va a la DLQ) y no retiene a los demas.
func TestUnEventoQueNoSeGuardaQuedaPendienteYNoRetieneALosDemas(t *testing.T) {
	s := newStack(t)
	bus := s.bus()
	src := s.source()
	bad := uuid.New()
	c := s.consumer(bus, failingFor{inner: s.uc, id: bad}, &countingInspector{})
	run(t, c)
	defer c.Stop()

	subject := s.subject("user.logged_in")
	if err := bus.PublishPersistent(subject, s.event(bad)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if err := bus.PublishPersistent(subject, s.event(uuid.New())); err != nil {
			t.Fatal(err)
		}
	}
	s.waitRecorded(4)

	deadline := time.Now().Add(10 * time.Second)
	for {
		info, err := s.js.ConsumerInfo(src.Stream, src.Durable)
		if err != nil {
			t.Fatal(err)
		}
		if info.NumAckPending == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pendientes de confirmar %d, se esperaba 1 (el que no se guardo)", info.NumAckPending)
		}
		time.Sleep(50 * time.Millisecond)
	}
	var found bool
	if err := s.admin.QueryRow(context.Background(), `SELECT EXISTS (SELECT 1 FROM audit.audit_logs WHERE id = $1)`, bad).Scan(&found); err != nil || found {
		t.Fatalf("el evento que fallo aparece en el rastro: %v %v", found, err)
	}
}
