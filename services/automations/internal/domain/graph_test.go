package domain

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func waitStep(id, next string) Step { return Step{ID: id, Type: StepWait, Duration: "1d", Next: next} }

func sendStepID(id, next string) Step {
	s := sendStep()
	s.ID, s.Next = id, next
	return s
}

func listStep(id, next string) Step {
	return Step{ID: id, Type: StepAddToList, ListID: ptr(uuid.New()), Next: next}
}

func branchStep(id string, c Condition, then, els string) Step {
	return Step{ID: id, Type: StepBranch, Condition: &c, Then: then, Else: els}
}

func opened(step string) Condition { return Condition{Kind: ConditionEmailOpened, Step: step} }

func expectGraphError(t *testing.T, name string, steps []Step, fragment string) {
	t.Helper()
	err := ValidateSteps(steps)
	if !errors.Is(err, ErrInvalidInput) {
		t.Errorf("%s: se esperaba un error de validacion, hubo %v", name, err)
		return
	}
	if fragment != "" && !strings.Contains(err.Error(), fragment) {
		t.Errorf("%s: el error no nombra %q: %v", name, fragment, err)
	}
}

func TestFlujoLinealAnteriorSeLeeComoCadena(t *testing.T) {
	steps := []Step{{Type: StepWait, Duration: "1d"}, sendStep(), {Type: StepAddToList, ListID: ptr(uuid.New())}}
	if err := ValidateSteps(steps); err != nil {
		t.Fatal(err)
	}
	for i, want := range []struct{ id, next string }{{"s1", "s2"}, {"s2", "s3"}, {"s3", ""}} {
		if steps[i].ID != want.id || steps[i].Next != want.next {
			t.Fatalf("paso %d: %+v", i, steps[i])
		}
	}
	for i := range steps {
		if got := NextIndex(steps, i, false); got != i+1 {
			t.Fatalf("paso %d sigue en %d", i, got)
		}
	}
	// Veinte pasos lineales (el tope anterior) siguen siendo validos.
	legacy := make([]Step, 20)
	for i := range legacy {
		legacy[i] = Step{Type: StepWait, Duration: "1m"}
	}
	if err := ValidateSteps(legacy); err != nil {
		t.Fatalf("un flujo lineal de 20 pasos debe seguir valiendo: %v", err)
	}
	// Una lista que ya tiene ids no se reescribe.
	mixed := []Step{waitStep("a", ""), {Type: StepWait, Duration: "1d"}}
	UpgradeLegacySteps(mixed)
	if mixed[1].ID != "" {
		t.Fatal("una lista con algun id no se toma por anterior")
	}
	expectGraphError(t, "ids a medias", mixed, "steps[1].id")
}

func TestGrafoConRamas(t *testing.T) {
	steps := []Step{
		sendStepID("bienvenida", "espera"),
		waitStep("espera", "abrio"),
		branchStep("abrio", opened("bienvenida"), "vip", "recordatorio"),
		listStep("vip", ""),
		sendStepID("recordatorio", "fin"),
		listStep("fin", ""),
	}
	if err := ValidateSteps(steps); err != nil {
		t.Fatal(err)
	}
	if NextIndex(steps, 2, true) != 3 || NextIndex(steps, 2, false) != 4 {
		t.Fatal("la rama sigue por then o por else")
	}
	if NextIndex(steps, 3, false) != len(steps) || NextIndex(steps, 5, false) != len(steps) {
		t.Fatal("un destino vacio es el fin")
	}
	if NextIndex(steps, 99, false) != len(steps) || NextIndex(steps, -1, false) != len(steps) {
		t.Fatal("una posicion fuera del flujo es el fin")
	}
	// Dos ramas que convergen en el mismo paso.
	merge := []Step{
		branchStep("r", Condition{Kind: ConditionSegment, SegmentID: ptr(uuid.New())}, "a", "b"),
		listStep("a", "c"),
		listStep("b", "c"),
		sendStepID("c", ""),
	}
	if err := ValidateSteps(merge); err != nil {
		t.Fatalf("las ramas pueden converger: %v", err)
	}
	// Una rama con las dos salidas al fin es valida (decide si seguir o no).
	if err := ValidateSteps([]Step{branchStep("r", Condition{Kind: ConditionSegment, SegmentID: ptr(uuid.New())}, "", "")}); err != nil {
		t.Fatal(err)
	}
}

func TestGrafoInvalido(t *testing.T) {
	seg := Condition{Kind: ConditionSegment, SegmentID: ptr(uuid.New())}
	expectGraphError(t, "ciclo", []Step{waitStep("a", "b"), waitStep("b", "c"), waitStep("c", "a")}, "ciclo")
	expectGraphError(t, "ciclo por una rama", []Step{branchStep("r", seg, "a", ""), waitStep("a", "r")}, "ciclo")
	expectGraphError(t, "a si mismo", []Step{waitStep("a", "a")}, "si mismo")
	expectGraphError(t, "destino inexistente", []Step{waitStep("a", "z")}, `"z"`)
	expectGraphError(t, "id repetido", []Step{waitStep("a", "a2"), waitStep("a2", ""), waitStep("a", "")}, "repite")
	expectGraphError(t, "id vacio", []Step{waitStep("a", ""), {Type: StepWait, Duration: "1d", Next: "a"}}, "steps[1].id")
	expectGraphError(t, "id con mayusculas", []Step{waitStep("Alta", "")}, "steps[0].id")
	expectGraphError(t, "id largo", []Step{waitStep(strings.Repeat("a", MaxStepIDLen+1), "")}, "steps[0].id")
	expectGraphError(t, "inalcanzable", []Step{waitStep("a", ""), waitStep("b", "")}, "steps[1] no es alcanzable")
	expectGraphError(t, "misma salida", []Step{branchStep("r", seg, "a", "a"), waitStep("a", "")}, "mismo paso")
	expectGraphError(t, "rama con next", []Step{{ID: "r", Type: StepBranch, Condition: &seg, Next: "a"}, waitStep("a", "")}, "next")
	expectGraphError(t, "rama sin condicion", []Step{{ID: "r", Type: StepBranch}}, "condition")
	expectGraphError(t, "then en un paso normal", []Step{{ID: "a", Type: StepWait, Duration: "1d", Then: "b"}, waitStep("b", "")}, "branch")
	expectGraphError(t, "condicion en un paso normal", []Step{{ID: "a", Type: StepWait, Duration: "1d", Condition: &seg}}, "branch")
	expectGraphError(t, "rama con lista", []Step{{ID: "r", Type: StepBranch, Condition: &seg, ListID: ptr(uuid.New())}}, "branch")

	tooMany := make([]Step, MaxSteps+1)
	for i := range tooMany {
		tooMany[i] = Step{Type: StepWait, Duration: "1m"}
	}
	expectGraphError(t, "demasiados pasos", tooMany, "steps")

	deep := make([]Step, MaxDepth+1)
	for i := range deep {
		next := ""
		if i+1 < len(deep) {
			next = "p" + strconv.Itoa(i+1)
		}
		deep[i] = waitStep("p"+strconv.Itoa(i), next)
	}
	expectGraphError(t, "demasiado profundo", deep, "máximo")
	if err := ValidateSteps(deep[:MaxDepth]); err == nil {
		t.Fatal("con el ultimo paso apuntando a uno que ya no existe debe fallar")
	}
	deep[MaxDepth-1].Next = ""
	if err := ValidateSteps(deep[:MaxDepth]); err != nil {
		t.Fatalf("MaxDepth pasos en fila valen: %v", err)
	}
}

// Un flujo ancho no es profundo: 40 pasos repartidos en ramas caben aunque ninguno de sus
// recorridos pase de MaxDepth.
func TestGrafoAnchoCabeAunqueSeaGrande(t *testing.T) {
	steps := []Step{branchStep("r", Condition{Kind: ConditionSegment, SegmentID: ptr(uuid.New())}, "a0", "b0")}
	for _, side := range []string{"a", "b"} {
		for i := 0; i < 19; i++ {
			next := side + strconv.Itoa(i+1)
			if i == 18 {
				next = ""
			}
			steps = append(steps, waitStep(side+strconv.Itoa(i), next))
		}
	}
	if len(steps) != 39 {
		t.Fatalf("pasos: %d", len(steps))
	}
	if err := ValidateSteps(steps); err != nil {
		t.Fatal(err)
	}
}

func TestRamaSobreUnCorreoAnterior(t *testing.T) {
	// El envio debe dominar la rama: todo recorrido que llega a ella pasa por el.
	good := []Step{sendStepID("e", "r"), branchStep("r", opened("e"), "", "")}
	if err := ValidateSteps(good); err != nil {
		t.Fatal(err)
	}
	seg := Condition{Kind: ConditionSegment, SegmentID: ptr(uuid.New())}
	notOnEveryPath := []Step{
		branchStep("s", seg, "e", "x"),
		sendStepID("e", "r"),
		waitStep("x", "r"),
		branchStep("r", opened("e"), "", ""),
	}
	expectGraphError(t, "envio en una sola rama", notOnEveryPath, "todo recorrido")
	after := []Step{branchStep("r", opened("e"), "e", ""), sendStepID("e", "")}
	expectGraphError(t, "envio posterior", after, "todo recorrido")
	notSend := []Step{waitStep("w", "r"), branchStep("r", opened("w"), "", "")}
	expectGraphError(t, "no es un envio", notSend, "send_email")
	missing := []Step{sendStepID("e", "r"), branchStep("r", opened("zz"), "", "")}
	expectGraphError(t, "paso inexistente", missing, "no existe")
	clicked := []Step{sendStepID("e", "r"), branchStep("r", Condition{Kind: ConditionEmailClicked, Step: "e"}, "", "")}
	if err := ValidateSteps(clicked); err != nil {
		t.Fatal(err)
	}
}

func TestCondiciones(t *testing.T) {
	seg := ptr(uuid.New())
	valid := []Condition{
		{Kind: ConditionSegment, SegmentID: seg},
		{Kind: ConditionAttribute, Attribute: "plan", Op: "eq", Value: json.RawMessage(` "oro" `)},
		{Kind: ConditionAttribute, Attribute: "plan", Op: "exists"},
		{Kind: ConditionAttribute, Attribute: "plan", Op: "exists", Value: json.RawMessage(`null`)},
	}
	for i, c := range valid {
		if err := ValidateSteps([]Step{branchStep("r", c, "", "")}); err != nil {
			t.Errorf("valida %d: %v", i, err)
		}
	}
	invalid := map[string]Condition{
		"sin tipo":              {},
		"tipo desconocido":      {Kind: "opened_today"},
		"segmento sin id":       {Kind: ConditionSegment},
		"segmento nulo":         {Kind: ConditionSegment, SegmentID: ptr(uuid.Nil)},
		"segmento con paso":     {Kind: ConditionSegment, SegmentID: seg, Step: "e"},
		"atributo sin clave":    {Kind: ConditionAttribute, Op: "eq"},
		"atributo mal escrito":  {Kind: ConditionAttribute, Attribute: "Plan", Op: "eq"},
		"atributo sin op":       {Kind: ConditionAttribute, Attribute: "plan"},
		"op con simbolos":       {Kind: ConditionAttribute, Attribute: "plan", Op: "=="},
		"valor no JSON":         {Kind: ConditionAttribute, Attribute: "plan", Op: "eq", Value: json.RawMessage(`{`)},
		"valor enorme":          {Kind: ConditionAttribute, Attribute: "plan", Op: "eq", Value: json.RawMessage(`"` + strings.Repeat("a", MaxConditionValueBytes) + `"`)},
		"atributo con segmento": {Kind: ConditionAttribute, Attribute: "plan", Op: "eq", SegmentID: seg},
		"apertura sin paso":     {Kind: ConditionEmailOpened},
		"apertura con atributo": {Kind: ConditionEmailOpened, Step: "e", Attribute: "plan"},
	}
	for name, c := range invalid {
		steps := []Step{sendStepID("e", "r"), branchStep("r", c, "", "")}
		expectGraphError(t, name, steps, "steps[1].condition")
	}
	c := Condition{Kind: ConditionAttribute, Attribute: "plan", Op: "in", Value: json.RawMessage(`["oro","plata"]`)}
	var def map[string]any
	if err := json.Unmarshal(c.AttributeDefinition(), &def); err != nil {
		t.Fatal(err)
	}
	rule := def["rules"].([]any)[0].(map[string]any)
	if def["match"] != "all" || rule["field"] != "attributes.plan" || rule["op"] != "in" || len(rule["value"].([]any)) != 2 {
		t.Fatalf("definicion del DSL: %v", def)
	}
	noValue := Condition{Kind: ConditionAttribute, Attribute: "plan", Op: "exists"}
	if strings.Contains(string(noValue.AttributeDefinition()), "value") {
		t.Fatal("sin valor la regla no lleva value")
	}
}

func TestCopiaDeCondiciones(t *testing.T) {
	c := Condition{Kind: ConditionAttribute, Attribute: "plan", Op: "eq", Value: json.RawMessage(`"oro"`)}
	steps := cloneSteps([]Step{branchStep("r", c, "", "")})
	steps[0].Condition.Value[1] = 'X'
	if string(c.Value) != `"oro"` {
		t.Fatal("editar la copia no altera el original")
	}
}

func TestDisparadorPorFecha(t *testing.T) {
	base := NewWorkflowInput{Name: "Cumple", Steps: []Step{sendStep()}, CreatedBy: uuid.New()}
	good := Trigger{Type: TriggerContactDate, Attribute: " cumple ", Hour: ptr(0), Timezone: " America/Lima "}
	in := base
	in.Trigger = good
	w, err := NewWorkflow(uuid.New(), in)
	if err != nil {
		t.Fatal(err)
	}
	if w.Trigger.Attribute != "cumple" || w.Trigger.Timezone != "America/Lima" {
		t.Fatalf("se normaliza: %+v", w.Trigger)
	}
	for name, tr := range map[string]Trigger{
		"sin atributo":      {Type: TriggerContactDate, Hour: ptr(9), Timezone: "UTC"},
		"atributo invalido": {Type: TriggerContactDate, Attribute: "Cumple", Hour: ptr(9), Timezone: "UTC"},
		"sin hora":          {Type: TriggerContactDate, Attribute: "cumple", Timezone: "UTC"},
		"hora 24":           {Type: TriggerContactDate, Attribute: "cumple", Hour: ptr(24), Timezone: "UTC"},
		"hora negativa":     {Type: TriggerContactDate, Attribute: "cumple", Hour: ptr(-1), Timezone: "UTC"},
		"sin zona":          {Type: TriggerContactDate, Attribute: "cumple", Hour: ptr(9)},
		"zona inexistente":  {Type: TriggerContactDate, Attribute: "cumple", Hour: ptr(9), Timezone: "Marte/Olimpo"},
		"zona del servidor": {Type: TriggerContactDate, Attribute: "cumple", Hour: ptr(9), Timezone: "Local"},
		"campos en otro":    {Type: TriggerContactCreated, Hour: ptr(9)},
		"atributo en otro":  {Type: TriggerEmailClicked, Attribute: "cumple"},
		"campana en fechas": {Type: TriggerContactDate, Attribute: "cumple", Hour: ptr(9), Timezone: "UTC", CampaignID: ptr(uuid.New())},
		"zona en un alta":   {Type: TriggerContactCreated, Timezone: "UTC"},
		"hora 23 sin zona":  {Type: TriggerContactDate, Attribute: "cumple", Hour: ptr(23)},
	} {
		in := base
		in.Trigger = tr
		if _, err := NewWorkflow(uuid.New(), in); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Un disparador por fecha nunca entra por un evento.
	w.Status = StatusActive
	if w.Accepts(TriggerEvent{EventID: "e", Type: TriggerContactDate, ContactID: uuid.New()}) {
		t.Fatal("contact.date no entra por eventos")
	}
}

func TestEntradaPorAniversario(t *testing.T) {
	w := &Workflow{ID: uuid.New(), TenantID: uuid.New(), ReEntry: true}
	contact := uuid.New()
	now := time.Date(2028, 2, 29, 9, 0, 0, 0, time.UTC)
	r, err := NewDateRun(w, contact, "2028-02-29", now)
	if err != nil {
		t.Fatal(err)
	}
	if r.EntryKey != "date:2028" || r.TriggerEventID != "date:2028-02-29" || r.ContactID != contact || !r.NextRunAt.Equal(now) || r.Status != RunWaiting {
		t.Fatalf("con reentrada, una vez al ano: %+v", r)
	}
	again, _ := NewDateRun(w, contact, "2028-02-29", now.Add(time.Hour))
	if again.EntryKey != r.EntryKey || again.TriggerEventID != r.TriggerEventID {
		t.Fatal("un segundo recorrido del mismo dia da la misma clave")
	}
	next, _ := NewDateRun(w, contact, "2029-02-28", now.AddDate(1, 0, 0))
	if next.EntryKey == r.EntryKey {
		t.Fatal("el ano siguiente es otra entrada")
	}
	w.ReEntry = false
	if once, _ := NewDateRun(w, contact, "2028-02-29", now); once.EntryKey != EntryOnce {
		t.Fatal("sin reentrada, una sola vez")
	}
	for _, bad := range []string{"", "2028-13-01", "29/02/2028", "2027-02-29"} {
		if _, err := NewDateRun(w, contact, bad, now); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if _, err := NewDateRun(w, uuid.Nil, "2028-02-29", now); !errors.Is(err, ErrInvalidInput) {
		t.Fatal("sin contacto")
	}
}

func TestMensajeCumpleLaCondicion(t *testing.T) {
	at := time.Now()
	var none *RunMessage
	if none.Satisfies(ConditionEmailOpened) {
		t.Fatal("sin correo no hay apertura")
	}
	sent := &RunMessage{}
	if sent.Satisfies(ConditionEmailOpened) || sent.Satisfies(ConditionEmailClicked) {
		t.Fatal("enviado sin abrir")
	}
	open := &RunMessage{OpenedAt: &at}
	if !open.Satisfies(ConditionEmailOpened) || open.Satisfies(ConditionEmailClicked) {
		t.Fatal("abierto sin clic")
	}
	click := &RunMessage{ClickedAt: &at}
	if !click.Satisfies(ConditionEmailOpened) || !click.Satisfies(ConditionEmailClicked) {
		t.Fatal("un clic cuenta como apertura")
	}
	if click.Satisfies(ConditionSegment) {
		t.Fatal("un correo no responde a una condicion de segmento")
	}
}
