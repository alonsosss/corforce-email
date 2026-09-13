package domain

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func ptr[T any](v T) *T { return &v }

func sendStep() Step {
	return Step{Type: StepSendEmail, TemplateID: ptr(uuid.New()), FromEmail: "news@Shop.Example.com", FromName: " Tienda "}
}

func TestParseWait(t *testing.T) {
	ok := map[string]time.Duration{"1m": time.Minute, "90d": 90 * 24 * time.Hour, "1440m": 24 * time.Hour, "12h": 12 * time.Hour}
	for in, want := range ok {
		got, err := ParseWait(in)
		if err != nil || got != want {
			t.Errorf("%s: %v %v", in, got, err)
		}
	}
	for _, in := range []string{"", "0m", "91d", "2161h", "30s", "1h30m", "-1d", "01d", "1D", "9999999d"} {
		if _, err := ParseWait(in); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%q debe rechazarse: %v", in, err)
		}
	}
}

func TestValidateSteps(t *testing.T) {
	list := ptr(uuid.New())
	cases := map[string][]Step{
		"vacio":                    {},
		"espera con plantilla":     {{Type: StepWait, Duration: "1d", TemplateID: ptr(uuid.New())}},
		"envio sin plantilla":      {{Type: StepSendEmail, FromEmail: "a@shop.example.com"}},
		"envio con remitente malo": {{Type: StepSendEmail, TemplateID: ptr(uuid.New()), FromEmail: "no es correo"}},
		"envio con version cero":   {{Type: StepSendEmail, TemplateID: ptr(uuid.New()), TemplateVersion: ptr(0), FromEmail: "a@shop.example.com"}},
		"envio con lista":          {{Type: StepSendEmail, TemplateID: ptr(uuid.New()), FromEmail: "a@shop.example.com", ListID: list}},
		"lista sin id":             {{Type: StepAddToList}},
		"lista con espera":         {{Type: StepRemoveFromList, ListID: list, Duration: "1d"}},
		"tipo desconocido":         {{Type: "branch"}},
	}
	for name, steps := range cases {
		if err := ValidateSteps(steps); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: se esperaba un error de validacion, hubo %v", name, err)
		}
	}
	many := make([]Step, MaxSteps+1)
	for i := range many {
		many[i] = Step{Type: StepWait, Duration: "1m"}
	}
	if err := ValidateSteps(many); err == nil {
		t.Fatal("mas de 20 pasos")
	}
	good := []Step{{Type: StepWait, Duration: "1d"}, sendStep(), {Type: StepAddToList, ListID: list}}
	if err := ValidateSteps(good); err != nil {
		t.Fatal(err)
	}
	if good[1].FromEmail != "news@shop.example.com" || good[1].FromName != "Tienda" {
		t.Fatalf("el remitente se normaliza: %+v", good[1])
	}
	if err := ValidateSteps([]Step{{Type: StepWait, Duration: "1d"}, {Type: StepAddToList}}); err == nil ||
		!strings.Contains(err.Error(), "steps[1]") {
		t.Fatalf("el error nombra el paso: %v", err)
	}
}

func TestTriggers(t *testing.T) {
	base := NewWorkflowInput{Name: "F", Steps: []Step{sendStep()}, CreatedBy: uuid.New()}
	for name, tr := range map[string]Trigger{
		"desconocido":         {Type: "contact.deleted"},
		"campana sin clic":    {Type: TriggerContactCreated, CampaignID: ptr(uuid.New())},
		"campana vacia":       {Type: TriggerEmailClicked, CampaignID: ptr(uuid.Nil)},
		"tipo vacio":          {},
		"mayusculas no valen": {Type: "Contact.Created"},
	} {
		in := base
		in.Trigger = tr
		if _, err := NewWorkflow(uuid.New(), in); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, tt := range TriggerTypes() {
		in := base
		in.Trigger = Trigger{Type: tt}
		if _, err := NewWorkflow(uuid.New(), in); err != nil {
			t.Errorf("%s: %v", tt, err)
		}
	}
}

func TestCicloDeVidaDelFlujo(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	w, err := NewWorkflow(uuid.New(), NewWorkflowInput{
		Name: "  Bienvenida ", Trigger: Trigger{Type: TriggerContactCreated}, Steps: []Step{sendStep()}, CreatedBy: uuid.New(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if w.Status != StatusDraft || w.Name != "Bienvenida" {
		t.Fatalf("nace en borrador con el nombre normalizado: %+v", w)
	}
	if err := w.Pause("x"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("un borrador no se pausa: %v", err)
	}
	if err := w.Activate(now); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("sin version fijada no se activa: %v", err)
	}
	w.Steps[0].TemplateVersion = ptr(2)
	if err := w.Activate(now); err != nil || w.Status != StatusActive || w.ActivatedAt == nil {
		t.Fatalf("activar: %v %+v", err, w)
	}
	name := "Otro"
	if err := w.ApplyPatch(Patch{Name: &name}); !errors.Is(err, ErrNotEditable) {
		t.Fatalf("activo no se edita: %v", err)
	}
	if w.Deletable() {
		t.Fatal("activo no se borra")
	}
	if err := w.Pause("revision"); err != nil || w.PauseReason != "revision" {
		t.Fatalf("pausar: %v", err)
	}
	if err := w.ApplyPatch(Patch{}); !errors.Is(err, ErrNothingToUpdate) {
		t.Fatalf("parche vacio: %v", err)
	}
	bad := []Step{}
	if err := w.ApplyPatch(Patch{Steps: bad}); !errors.Is(err, ErrInvalidInput) || len(w.Steps) != 1 {
		t.Fatalf("un parche invalido no deja el flujo a medias: %v %+v", err, w.Steps)
	}
	if err := w.ApplyPatch(Patch{Name: &name, SetListID: true, ListID: ptr(uuid.New())}); err != nil || w.Name != "Otro" || w.ListID == nil {
		t.Fatalf("pausado se edita: %v", err)
	}
	if err := w.Activate(now); err != nil || w.PauseReason != "" {
		t.Fatalf("reactivar limpia el motivo: %v %q", err, w.PauseReason)
	}
	if err := w.Archive(); err != nil || !w.Deletable() {
		t.Fatalf("archivar: %v", err)
	}
	if err := w.Archive(); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("archivar dos veces: %v", err)
	}
	if err := w.Activate(now); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("un archivado no se reactiva: %v", err)
	}
}

func TestAcceptsFiltraPorDisparadorYCampana(t *testing.T) {
	campaign := uuid.New()
	w := &Workflow{ID: uuid.New(), Status: StatusActive, Trigger: Trigger{Type: TriggerEmailClicked}}
	contact := uuid.New()
	click := func(c *uuid.UUID) TriggerEvent {
		return TriggerEvent{EventID: "e", Type: TriggerEmailClicked, ContactID: contact, CampaignID: c}
	}
	if !w.Accepts(click(&campaign)) {
		t.Fatal("sin campana concreta acepta cualquier clic")
	}
	if w.Accepts(click(&w.ID)) {
		t.Fatal("un clic en un correo del propio flujo no lo vuelve a disparar")
	}
	w.Trigger.CampaignID = &campaign
	other := uuid.New()
	if w.Accepts(click(&other)) || w.Accepts(click(nil)) || !w.Accepts(click(&campaign)) {
		t.Fatal("con campana concreta solo acepta sus clics")
	}
	if w.Accepts(TriggerEvent{Type: TriggerContactCreated, ContactID: contact}) {
		t.Fatal("otro disparador no entra")
	}
	w.Status = StatusPaused
	if w.Accepts(click(&campaign)) {
		t.Fatal("un flujo pausado no acepta")
	}
}

func TestEjecucionClaveYEntrada(t *testing.T) {
	w := &Workflow{ID: uuid.New(), TenantID: uuid.New()}
	ev := TriggerEvent{EventID: "evt-1", ContactID: uuid.New()}
	now := time.Now().UTC()
	r := NewRun(w, ev, now)
	if r.EntryKey != EntryOnce || r.Status != RunWaiting || !r.NextRunAt.Equal(now) {
		t.Fatalf("sin reentrada la clave es once: %+v", r)
	}
	w.ReEntry = true
	if NewRun(w, ev, now).EntryKey != "evt-1" {
		t.Fatal("con reentrada la clave es el evento")
	}
	r.StepIndex = 3
	if got := r.IdempotencyKey(); got != "automation:"+r.ID.String()+":step:3" {
		t.Fatalf("clave: %s", got)
	}
}

func TestEsperas(t *testing.T) {
	prev := time.Duration(0)
	for i := 1; i <= 20; i++ {
		d := RetryBackoff(i)
		if d < prev || d > maxBackoff {
			t.Fatalf("backoff %d = %s", i, d)
		}
		prev = d
	}
	if ClampRetryAfter(0) != defaultRetryAfter || ClampRetryAfter(time.Second) != minRetryAfter || ClampRetryAfter(48*time.Hour) != maxRetryAfter {
		t.Fatal("el Retry-After se acota")
	}
}

func TestDOI(t *testing.T) {
	if err := (DOILimits{PerDay: 2, Per30Days: 1}).Validate(); err == nil {
		t.Fatal("por dia no puede superar a 30 dias")
	}
	l := DOILimits{PerDay: 1, Per30Days: 3}
	if !l.Allows(0, 2) || l.Allows(1, 1) || l.Allows(0, 3) {
		t.Fatal("limites")
	}
	s := DOISettings{Enabled: true}
	if err := s.Normalize(); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("activado exige plantilla: %v", err)
	}
	s = DOISettings{Enabled: true, TemplateID: ptr(uuid.New()), FromEmail: "Hola@Shop.Example.com"}
	if err := s.Normalize(); err != nil || s.FromEmail != "Hola@shop.example.com" || !s.Ready() {
		t.Fatalf("normaliza: %v %+v", err, s)
	}
	off := DOISettings{ReplyTo: "no-valido"}
	if err := off.Normalize(); err == nil {
		t.Fatal("desactivado tambien valida lo que trae")
	}
	req := ConsentRequest{EventID: "e", TenantID: uuid.New(), ContactID: uuid.New(), Email: "ana@example.com", ConfirmURL: "javascript:alert(1)"}
	if err := req.Validate(); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("un enlace que no es http(s) se rechaza: %v", err)
	}
	req.ConfirmURL = "https://app.example.com/c?t=1&k=2"
	if err := req.Validate(); err != nil {
		t.Fatal(err)
	}
	d := NewDOIDelivery(req, &s, DOIPending, "")
	if d.IdempotencyKey() != "doi:e" || d.TemplateID == nil || d.FromEmail == "" {
		t.Fatalf("la entrega lleva la foto de los ajustes: %+v", d)
	}
}
