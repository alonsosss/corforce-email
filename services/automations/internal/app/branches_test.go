package app

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/automations/internal/domain"
	"github.com/alonsosss/corforce-email/services/automations/internal/ports"
	"github.com/google/uuid"
)

func sendAs(id, next string) domain.Step {
	s := sendEmail()
	s.ID, s.Next = id, next
	return s
}

func listAs(id string, list uuid.UUID) domain.Step {
	return domain.Step{ID: id, Type: domain.StepAddToList, ListID: &list}
}

func branchOn(c domain.Condition, then, els string) domain.Step {
	return domain.Step{ID: "rama", Type: domain.StepBranch, Condition: &c, Then: then, Else: els}
}

// messageOf es el mensaje que transactional creo para el paso de envio de la ejecucion.
func (f *fixture) messageOf(t *testing.T, runID uuid.UUID, stepID string) domain.RunMessage {
	t.Helper()
	m, err := f.messages.Get(ctx, f.tenant, runID, stepID)
	if err != nil || m == nil {
		t.Fatalf("sin correo del paso %s: %v", stepID, err)
	}
	return *m
}

// Bienvenida, espera de un dia y rama por apertura: quien abrio entra en la lista vip y
// quien no, en la de recordatorio.
func TestRamaPorAperturaDelCorreoAnterior(t *testing.T) {
	f := newFixture(t, Config{})
	vip, rec := uuid.New(), uuid.New()
	w := f.activeWorkflow(t, contactCreated, false,
		sendAs("bienvenida", "espera"),
		domain.Step{ID: "espera", Type: domain.StepWait, Duration: "1d", Next: "rama"},
		branchOn(domain.Condition{Kind: domain.ConditionEmailOpened, Step: "bienvenida"}, "vip", "recordatorio"),
		listAs("vip", vip), listAs("recordatorio", rec))
	abre, noAbre := f.sendable("abre@example.com"), f.sendable("no-abre@example.com")
	f.enter(t, abre.ID)
	f.enter(t, noAbre.ID)

	f.tick(t) // envio
	var abreRun domain.Run
	for _, r := range f.store.RunsOf(w.ID) {
		if r.ContactID == abre.ID {
			abreRun = r
		}
	}
	m := f.messageOf(t, abreRun.ID, "bienvenida")
	if m.WorkflowID != w.ID || m.ContactID != abre.ID {
		t.Fatalf("correo del paso: %+v", m)
	}
	// La apertura llega por transactional.email.opened; una reentrega no cambia nada.
	at := f.clock.Now().Add(time.Hour)
	for i := 0; i < 2; i++ {
		ok, err := f.uc.RecordMessageEngagement(ctx, f.tenant, m.MessageID, false, at)
		if err != nil || !ok {
			t.Fatalf("apertura: %v %v", ok, err)
		}
	}
	if ok, _ := f.uc.RecordMessageEngagement(ctx, f.tenant, uuid.New(), false, at); ok {
		t.Fatal("un mensaje ajeno a los flujos no se anota")
	}

	f.tick(t) // espera
	f.clock.Advance(24 * time.Hour)
	f.tick(t) // rama
	f.tick(t) // lista
	if !f.contacts.IsMember(vip, abre.ID) || f.contacts.IsMember(rec, abre.ID) {
		t.Fatal("quien abrio sigue por then")
	}
	if !f.contacts.IsMember(rec, noAbre.ID) || f.contacts.IsMember(vip, noAbre.ID) {
		t.Fatal("quien no abrio sigue por else")
	}
	for _, r := range f.store.RunsOf(w.ID) {
		if r.Status != domain.RunCompleted {
			t.Fatalf("recorrido terminado: %+v", r)
		}
	}
}

// El clic es mas estricto que la apertura, y un clic cuenta como apertura.
func TestRamaPorClic(t *testing.T) {
	for _, tc := range []struct {
		name    string
		kind    domain.ConditionKind
		clicked bool
		then    bool
	}{
		{"abrio sin clic, rama de clic", domain.ConditionEmailClicked, false, false},
		{"clic, rama de clic", domain.ConditionEmailClicked, true, true},
		{"clic, rama de apertura", domain.ConditionEmailOpened, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, Config{})
			yes, no := uuid.New(), uuid.New()
			w := f.activeWorkflow(t, contactCreated, false,
				sendAs("e", "rama"), branchOn(domain.Condition{Kind: tc.kind, Step: "e"}, "si", "no"),
				listAs("si", yes), listAs("no", no))
			c := f.sendable("ana@example.com")
			f.enter(t, c.ID)
			f.tick(t)
			run := f.onlyRun(t, w)
			m := f.messageOf(t, run.ID, "e")
			if _, err := f.uc.RecordMessageEngagement(ctx, f.tenant, m.MessageID, tc.clicked, f.clock.Now()); err != nil {
				t.Fatal(err)
			}
			f.tick(t)
			f.tick(t)
			if f.contacts.IsMember(yes, c.ID) != tc.then || f.contacts.IsMember(no, c.ID) == tc.then {
				t.Fatalf("rama equivocada")
			}
		})
	}
}

// Un envio que la supresion retiro no deja correo: la rama lo trata como no abierto.
func TestRamaSinCorreoVaPorElse(t *testing.T) {
	f := newFixture(t, Config{})
	yes, no := uuid.New(), uuid.New()
	w := f.activeWorkflow(t, contactCreated, false,
		sendAs("e", "rama"), branchOn(domain.Condition{Kind: domain.ConditionEmailOpened, Step: "e"}, "si", "no"),
		listAs("si", yes), listAs("no", no))
	c := f.sendable("ana@example.com")
	f.enter(t, c.ID)
	f.tick(t)
	run := f.onlyRun(t, w)
	f.messages.Rows = nil // como si transactional no hubiera creado mensaje
	f.tick(t)
	f.tick(t)
	if !f.contacts.IsMember(no, c.ID) || f.contacts.IsMember(yes, c.ID) {
		t.Fatalf("sin correo va por else: %+v", f.store.Run(run.ID))
	}
}

func TestRamaPorSegmentoYPorAtributo(t *testing.T) {
	f := newFixture(t, Config{})
	seg := uuid.New()
	in, out := f.sendable("in@example.com"), f.sendable("out@example.com")
	f.rules.Segments[seg] = map[uuid.UUID]bool{in.ID: true}
	yes, no := uuid.New(), uuid.New()
	w := f.activeWorkflow(t, contactCreated, false,
		branchOn(domain.Condition{Kind: domain.ConditionSegment, SegmentID: &seg}, "si", "no"), listAs("si", yes), listAs("no", no))
	f.enter(t, in.ID)
	f.enter(t, out.ID)
	f.tick(t)
	f.tick(t)
	if !f.contacts.IsMember(yes, in.ID) || !f.contacts.IsMember(no, out.ID) || f.contacts.IsMember(yes, out.ID) {
		t.Fatal("rama por segmento")
	}
	for _, r := range f.store.RunsOf(w.ID) {
		if r.Status != domain.RunCompleted {
			t.Fatalf("recorrido: %+v", r)
		}
	}
	last := f.rules.MatchCalls[len(f.rules.MatchCalls)-1]
	if last.SegmentID == nil || *last.SegmentID != seg || len(last.ContactIDs) != 1 {
		t.Fatalf("pregunta a contacts por un solo contacto: %+v", last)
	}

	cond := domain.Condition{Kind: domain.ConditionAttribute, Attribute: "plan", Op: "eq", Value: []byte(`"oro"`)}
	f.rules.Definitions[string(cond.AttributeDefinition())] = map[uuid.UUID]bool{out.ID: true}
	gold := uuid.New()
	f.activeWorkflow(t, contactCreated, false, branchOn(cond, "oro", ""), listAs("oro", gold))
	f.enter(t, out.ID)
	f.tick(t)
	f.tick(t)
	if !f.contacts.IsMember(gold, out.ID) {
		t.Fatal("rama por atributo")
	}
}

func TestSegmentoBorradoFallaLaEjecucion(t *testing.T) {
	f := newFixture(t, Config{})
	seg := uuid.New()
	f.rules.Segments[seg] = map[uuid.UUID]bool{}
	w := f.activeWorkflow(t, contactCreated, false,
		branchOn(domain.Condition{Kind: domain.ConditionSegment, SegmentID: &seg}, "", ""))
	c := f.sendable("ana@example.com")
	f.enter(t, c.ID)
	delete(f.rules.Segments, seg)
	f.tick(t)
	run := f.onlyRun(t, w)
	if run.Status != domain.RunFailed || run.ErrorCode != "NOT_FOUND" {
		t.Fatalf("segmento borrado: %+v", run)
	}
	f.rules.Segments[seg] = map[uuid.UUID]bool{}
	f.rules.MatchErr = ports.ErrUnavailable
	f.enter(t, f.sendable("b@example.com").ID)
	f.tick(t)
	for _, r := range f.store.RunsOf(w.ID) {
		if r.ContactID != c.ID && (r.Status != domain.RunWaiting || r.Attempts != 1) {
			t.Fatalf("contacts caido se reintenta: %+v", r)
		}
	}
}

func TestActivarCompruebaLasReglasEnContacts(t *testing.T) {
	f := newFixture(t, Config{})
	seg := uuid.New()
	w, err := f.uc.CreateWorkflow(ctx, f.tenant, domain.NewWorkflowInput{
		Name: "Rama", Trigger: contactCreated, CreatedBy: f.user,
		Steps: []domain.Step{branchOn(domain.Condition{Kind: domain.ConditionSegment, SegmentID: &seg}, "", "")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.uc.ActivateWorkflow(ctx, f.tenant, w.ID); !errors.Is(err, domain.ErrInvalidInput) || !strings.Contains(err.Error(), "steps[0].condition") {
		t.Fatalf("un segmento que no existe no se activa: %v", err)
	}
	f.rules.Segments[seg] = map[uuid.UUID]bool{}
	if _, err := f.uc.ActivateWorkflow(ctx, f.tenant, w.ID); err != nil {
		t.Fatal(err)
	}
	f.rules.MatchErr = ports.ErrUnavailable
	w2, _ := f.uc.CreateWorkflow(ctx, f.tenant, domain.NewWorkflowInput{
		Name: "Rama 2", Trigger: contactCreated, CreatedBy: f.user,
		Steps: []domain.Step{branchOn(domain.Condition{Kind: domain.ConditionSegment, SegmentID: &seg}, "", "")},
	})
	if _, err := f.uc.ActivateWorkflow(ctx, f.tenant, w2.ID); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatalf("contacts caido no activa ni da por valido: %v", err)
	}
}

// Una caida entre el envio y el avance no duplica el registro del correo.
func TestCorreoDelPasoSinDuplicarTrasUnaCaida(t *testing.T) {
	f := newFixture(t, Config{})
	w := f.activeWorkflow(t, contactCreated, false,
		sendAs("e", "rama"), branchOn(domain.Condition{Kind: domain.ConditionEmailOpened, Step: "e"}, "", ""))
	f.enter(t, f.sendable("ana@example.com").ID)
	f.store.FailAdvance = 1
	f.tick(t)
	f.clock.Advance(domain.RunLease + time.Second)
	f.tick(t)
	if len(f.messages.Rows) != 1 || f.sender.Created() != 1 || f.onlyRun(t, w).StepIndex != 1 {
		t.Fatalf("un correo registrado: %d filas, %d mensajes", len(f.messages.Rows), f.sender.Created())
	}
}

// Un flujo guardado antes de las ramas (sin ids) sigue recorriendose en orden.
func TestFlujoAnteriorSinIdsSigueEnOrden(t *testing.T) {
	f := newFixture(t, Config{})
	list := uuid.New()
	w := f.activeWorkflow(t, contactCreated, false, sendEmail(), domain.Step{Type: domain.StepAddToList, ListID: &list})
	stored := f.store.Workflow(w.ID)
	for i := range stored.Steps {
		stored.Steps[i].ID, stored.Steps[i].Next = "", ""
	}
	f.store.Workflows[w.ID] = stored
	c := f.sendable("ana@example.com")
	f.enter(t, c.ID)
	f.tick(t)
	f.tick(t)
	if !f.contacts.IsMember(list, c.ID) || f.onlyRun(t, w).Status != domain.RunCompleted {
		t.Fatalf("recorrido anterior: %+v", f.onlyRun(t, w))
	}
}

func dateTrigger(hour int) domain.Trigger {
	return domain.Trigger{Type: domain.TriggerContactDate, Attribute: "cumple", Hour: &hour, Timezone: "America/Lima"}
}

func TestDisparadorPorFechaUnaVezAlAno(t *testing.T) {
	f := newFixture(t, Config{DateScanInterval: 10 * time.Minute})
	list := uuid.New()
	a, b := uuid.New(), uuid.New()
	f.rules.Pages = []ports.AnniversaryPage{
		{Matches: []ports.AnniversaryMatch{{ContactID: a, Occurrence: "2026-09-13"}}, NextCursor: "c1"},
		{Matches: []ports.AnniversaryMatch{{ContactID: b, Occurrence: "2026-09-12"}, {ContactID: uuid.Nil, Occurrence: "x"}}},
	}
	w := f.activeWorkflow(t, dateTrigger(9), true, listAs("regalo", list))
	if n := len(f.rules.AnnivCalls); n != 1 || f.rules.AnnivCalls[0].Limit != 1 {
		t.Fatalf("activar comprueba el atributo en contacts: %+v", f.rules.AnnivCalls)
	}
	if n := f.enter(t, uuid.New()); n != 0 {
		t.Fatal("un flujo por fecha no entra por eventos")
	}
	n, err := f.uc.ScanDateTriggers(ctx, f.tenant)
	if err != nil || n != 2 {
		t.Fatalf("primer recorrido: %d %v", n, err)
	}
	q := f.rules.AnnivCalls[1]
	if q.Attribute != "cumple" || q.Hour != 9 || q.Timezone != "America/Lima" || q.Cursor != "" || f.rules.AnnivCalls[2].Cursor != "c1" {
		t.Fatalf("consulta: %+v", f.rules.AnnivCalls)
	}
	calls := len(f.rules.AnnivCalls)
	if n, _ := f.uc.ScanDateTriggers(ctx, f.tenant); n != 0 || len(f.rules.AnnivCalls) != calls {
		t.Fatal("dentro del intervalo no se vuelve a recorrer")
	}
	// Tras el intervalo (o un reinicio) el mismo aniversario no vuelve a entrar.
	f.clock.Advance(11 * time.Minute)
	if n, _ := f.uc.ScanDateTriggers(ctx, f.tenant); n != 0 || len(f.rules.AnnivCalls) == calls {
		t.Fatalf("mismo dia, nadie entra dos veces: %d", n)
	}
	// El ano siguiente es otra entrada.
	f.rules.Pages = []ports.AnniversaryPage{{Matches: []ports.AnniversaryMatch{{ContactID: a, Occurrence: "2027-09-13"}}}}
	f.clock.Advance(365 * 24 * time.Hour)
	if n, _ := f.uc.ScanDateTriggers(ctx, f.tenant); n != 1 {
		t.Fatalf("ano siguiente: %d", n)
	}
	if runs := f.store.RunsOf(w.ID); len(runs) != 3 {
		t.Fatalf("ejecuciones: %d", len(runs))
	}
}

func TestDisparadorPorFechaSinReentradaYConAtributoRetirado(t *testing.T) {
	f := newFixture(t, Config{})
	a := uuid.New()
	w := f.activeWorkflow(t, dateTrigger(0), false, listAs("regalo", uuid.New()))
	f.rules.Pages = []ports.AnniversaryPage{{Matches: []ports.AnniversaryMatch{{ContactID: a, Occurrence: "2026-09-13"}}}}
	if n, _ := f.uc.ScanDateTriggers(ctx, f.tenant); n != 1 {
		t.Fatal("primera vez")
	}
	f.rules.Pages = []ports.AnniversaryPage{{Matches: []ports.AnniversaryMatch{{ContactID: a, Occurrence: "2027-09-13"}}}}
	f.clock.Advance(365 * 24 * time.Hour)
	if n, _ := f.uc.ScanDateTriggers(ctx, f.tenant); n != 0 {
		t.Fatal("sin reentrada, una sola vez en la vida")
	}

	f.rules.AnniversaryErr = &ports.RejectedError{Status: 422, Code: "VALIDATION_ERROR", Message: "no es un atributo de fecha declarado"}
	f.clock.Advance(time.Hour)
	if _, err := f.uc.ScanDateTriggers(ctx, f.tenant); err != nil {
		t.Fatal(err)
	}
	got := f.store.Workflow(w.ID)
	if got.Status != domain.StatusPaused || !strings.HasPrefix(got.PauseReason, CodeDateTriggerInvalid) {
		t.Fatalf("atributo retirado pausa el flujo: %+v", got)
	}
	if len(f.store.Published("automations.workflow.paused")) != 1 {
		t.Fatal("la pausa se anuncia")
	}
}

func TestActivarDisparadorPorFechaInvalido(t *testing.T) {
	f := newFixture(t, Config{})
	f.rules.AnniversaryErr = &ports.RejectedError{Status: 422, Message: "no es un atributo de fecha declarado"}
	w, err := f.uc.CreateWorkflow(ctx, f.tenant, domain.NewWorkflowInput{
		Name: "Cumple", Trigger: dateTrigger(9), CreatedBy: f.user, Steps: []domain.Step{listAs("x", uuid.New())},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.uc.ActivateWorkflow(ctx, f.tenant, w.ID); !errors.Is(err, domain.ErrInvalidInput) || !strings.Contains(err.Error(), "trigger") {
		t.Fatalf("atributo no valido: %v", err)
	}
}
