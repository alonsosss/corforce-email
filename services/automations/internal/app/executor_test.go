package app

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/automations/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/automations/internal/domain"
	"github.com/alonsosss/corforce-email/services/automations/internal/ports"
	"github.com/google/uuid"
)

func TestEjecucionPasoAPaso(t *testing.T) {
	f := newFixture(t, Config{})
	c := f.sendable("ana@example.com")
	list := uuid.New()
	w := f.activeWorkflow(t, contactCreated, false,
		sendEmail(), domain.Step{Type: domain.StepWait, Duration: "2d"}, domain.Step{Type: domain.StepAddToList, ListID: &list})
	f.enter(t, c.ID)
	run := f.onlyRun(t, w)

	f.tick(t)
	if len(f.sender.MarketingCalls) != 1 {
		t.Fatalf("primer paso, un envio: %d", len(f.sender.MarketingCalls))
	}
	m := f.sender.MarketingCalls[0]
	if m.IdempotencyKey != "automation:"+run.ID.String()+":step:0" || m.WorkflowID != w.ID || m.TemplateVersion != 3 ||
		m.Contact.ID != c.ID || m.Tags["source"] != "automation" || m.Tags["workflow_id"] != w.ID.String() {
		t.Fatalf("envio: %+v", m)
	}
	var first string
	_ = json.Unmarshal(m.Contact.Variables()["first_name"], &first)
	if first != "Ana" {
		t.Fatalf("variables del contacto: %q", first)
	}
	// El paso de espera se hace en el siguiente tick y fija el siguiente a dos dias.
	f.tick(t)
	got := f.store.Run(run.ID)
	if got.StepIndex != 2 || got.Status != domain.RunWaiting || !got.NextRunAt.Equal(f.clock.Now().Add(48*time.Hour)) {
		t.Fatalf("tras la espera: %+v", got)
	}
	f.tick(t)
	if f.contacts.IsMember(list, c.ID) {
		t.Fatal("no se adelanta a la espera")
	}
	f.clock.Advance(48 * time.Hour)
	f.tick(t)
	got = f.store.Run(run.ID)
	if !f.contacts.IsMember(list, c.ID) || got.Status != domain.RunCompleted || got.FinishedAt == nil || got.LeaseToken != nil {
		t.Fatalf("ultimo paso y fin: %+v", got)
	}
	if n := len(f.store.Published("automations.run.completed")); n != 1 {
		t.Fatalf("evento de fin: %d", n)
	}
	if len(f.sender.MarketingCalls) != 1 {
		t.Fatal("un solo envio en todo el recorrido")
	}
}

// Una caida entre la respuesta de transactional y el commit deja la ejecucion reservada;
// al vencer la reserva otro trabajador repite el MISMO paso con la MISMA clave y
// transactional devuelve lo ya creado: un solo mensaje.
func TestReintentoTrasUnaCaidaUsaLaMismaClave(t *testing.T) {
	f := newFixture(t, Config{})
	c := f.sendable("ana@example.com")
	w := f.activeWorkflow(t, contactCreated, false, sendEmail(), domain.Step{Type: domain.StepWait, Duration: "1d"})
	f.enter(t, c.ID)

	f.store.FailAdvance = 1
	f.tick(t)
	run := f.onlyRun(t, w)
	if run.Status != domain.RunRunning || run.StepIndex != 0 || len(f.sender.MarketingCalls) != 1 {
		t.Fatalf("tras la caida sigue reservada en el mismo paso: %+v", run)
	}
	f.tick(t)
	if len(f.sender.MarketingCalls) != 1 {
		t.Fatal("con la reserva viva nadie repite el paso")
	}
	f.clock.Advance(domain.RunLease + time.Second)
	f.tick(t)
	if len(f.sender.MarketingCalls) != 2 || f.sender.Created() != 1 {
		t.Fatalf("dos llamadas, un mensaje: %d %d", len(f.sender.MarketingCalls), f.sender.Created())
	}
	if f.sender.MarketingCalls[0].IdempotencyKey != f.sender.MarketingCalls[1].IdempotencyKey {
		t.Fatal("misma clave en el reintento")
	}
	if got := f.onlyRun(t, w); got.StepIndex != 1 {
		t.Fatalf("avanza una sola vez: %+v", got)
	}
}

func TestUn429Reprograma(t *testing.T) {
	f := newFixture(t, Config{})
	c := f.sendable("ana@example.com")
	w := f.activeWorkflow(t, contactCreated, false, sendEmail())
	f.enter(t, c.ID)
	f.sender.MarketingErrs = []error{&ports.RateLimitedError{RetryAfter: 2 * time.Minute}}
	f.tick(t)
	run := f.onlyRun(t, w)
	if run.Status != domain.RunWaiting || run.StepIndex != 0 || run.Attempts != 0 || run.ErrorCode != domain.CodeRateLimited ||
		!run.NextRunAt.Equal(f.clock.Now().Add(2*time.Minute)) {
		t.Fatalf("reprogramada por lo que pide transactional: %+v", run)
	}
	f.clock.Advance(time.Minute)
	f.tick(t)
	if len(f.sender.MarketingCalls) != 1 {
		t.Fatal("no antes de tiempo")
	}
	f.clock.Advance(time.Minute)
	f.tick(t)
	if got := f.onlyRun(t, w); got.Status != domain.RunCompleted {
		t.Fatalf("despues sale: %+v", got)
	}
}

func TestContactoNoEnviableQuedaOmitido(t *testing.T) {
	f := newFixture(t, Config{})
	w := f.activeWorkflow(t, contactCreated, false, sendEmail())
	f.enter(t, uuid.New())
	f.tick(t)
	run := f.onlyRun(t, w)
	if run.Status != domain.RunSkipped || run.ErrorCode != domain.CodeContactNotSendable {
		t.Fatalf("omitida: %+v", run)
	}
	if len(f.sender.MarketingCalls) != 0 || len(f.store.Published("automations.run.failed")) != 0 {
		t.Fatal("no se envia y no es un fallo")
	}
}

func TestPausaTrasBloqueosRepetidos(t *testing.T) {
	f := newFixture(t, Config{PauseAfterFailures: 3})
	w := f.activeWorkflow(t, contactCreated, false, sendEmail())
	for i := 0; i < 4; i++ {
		f.enter(t, f.sendable(uuid.NewString()+"@example.com").ID)
	}
	blocked := &ports.BlockedError{Code: domain.CodeSendingRestricted, Message: "suspended"}
	f.sender.MarketingErrs = []error{blocked, blocked, blocked, blocked}
	f.tick(t)
	failed := 0
	for _, r := range f.store.RunsOf(w.ID) {
		if r.Status == domain.RunFailed && r.ErrorCode == domain.CodeSendingRestricted {
			failed++
		}
	}
	if failed != 3 {
		t.Fatalf("tres fallan antes de la pausa: %d", failed)
	}
	got := f.store.Workflow(w.ID)
	if got.Status != domain.StatusPaused || !strings.HasPrefix(got.PauseReason, domain.CodeSendingRestricted) {
		t.Fatalf("pausado con motivo: %+v", got)
	}
	if n := len(f.store.Published("automations.workflow.paused")); n != 1 {
		t.Fatalf("evento de pausa: %d", n)
	}
	if n := len(f.store.Published("automations.run.failed")); n != 3 {
		t.Fatalf("eventos de fallo: %d", n)
	}
	// La cuarta queda congelada, no fallida.
	waiting := 0
	for _, r := range f.store.RunsOf(w.ID) {
		if r.Status == domain.RunWaiting {
			waiting++
		}
	}
	if waiting != 1 {
		t.Fatalf("la restante espera: %d", waiting)
	}
}

func TestRechazoDefinitivoFalla(t *testing.T) {
	f := newFixture(t, Config{})
	c := f.sendable("ana@example.com")
	w := f.activeWorkflow(t, contactCreated, false, sendEmail())
	f.enter(t, c.ID)
	f.sender.MarketingErrs = []error{&ports.RejectedError{Status: 422, Code: "TEMPLATE_NOT_MARKETING", Message: "x"}}
	f.tick(t)
	run := f.onlyRun(t, w)
	if run.Status != domain.RunFailed || run.ErrorCode != "TEMPLATE_NOT_MARKETING" {
		t.Fatalf("fallida con el codigo: %+v", run)
	}
	if f.store.Workflow(w.ID).Status != domain.StatusActive {
		t.Fatal("un 422 no pausa el flujo")
	}
}

func TestFalloTransitorioReintentaConEsperaHastaAgotar(t *testing.T) {
	f := newFixture(t, Config{})
	c := f.sendable("ana@example.com")
	w := f.activeWorkflow(t, contactCreated, false, sendEmail())
	f.enter(t, c.ID)
	for i := 0; i < domain.MaxStepAttempts; i++ {
		f.sender.MarketingErrs = append(f.sender.MarketingErrs, ports.ErrUnavailable)
	}
	f.tick(t)
	run := f.onlyRun(t, w)
	if run.Status != domain.RunWaiting || run.Attempts != 1 || !run.NextRunAt.Equal(f.clock.Now().Add(domain.RetryBackoff(1))) {
		t.Fatalf("primer fallo: %+v", run)
	}
	for i := 0; i < domain.MaxStepAttempts; i++ {
		f.clock.Advance(time.Hour)
		f.tick(t)
	}
	run = f.onlyRun(t, w)
	if run.Status != domain.RunFailed || run.ErrorCode != domain.CodeUnavailable {
		t.Fatalf("agotada: %+v", run)
	}
	if len(f.sender.MarketingCalls) != domain.MaxStepAttempts {
		t.Fatalf("intentos: %d", len(f.sender.MarketingCalls))
	}
}

func TestLaReservaImpideQueDosTrabajadoresTomenLaMismaEjecucion(t *testing.T) {
	f := newFixture(t, Config{})
	c := f.sendable("ana@example.com")
	f.activeWorkflow(t, contactCreated, false, sendEmail())
	f.enter(t, c.ID)
	runs := apptest.Runs{S: f.store}
	now := f.clock.Now()
	a, _ := runs.ClaimDue(ctx, f.tenant, now, now.Add(domain.RunLease), 10)
	b, _ := runs.ClaimDue(ctx, f.tenant, now, now.Add(domain.RunLease), 10)
	if len(a) != 1 || len(b) != 0 {
		t.Fatalf("una sola reserva: %d %d", len(a), len(b))
	}
	stale := a[0]
	other := uuid.New()
	stale.LeaseToken = &other
	if ok, _ := runs.Advance(ctx, &stale, 1, now); ok {
		t.Fatal("sin la reserva no se escribe")
	}
}
