package domain

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

func TestTransicionesDeUnaEjecucion(t *testing.T) {
	now := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	running := func() *JobExecution {
		e := &JobExecution{ID: uuid.New(), Status: StatusPending, CreatedAt: now}
		e.Dispatch(now, 60)
		return e
	}

	e := running()
	if e.Status != StatusRunning || !e.DeadlineAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("despachada: %s, plazo %v", e.Status, e.DeadlineAt)
	}
	if changed, err := e.Complete(now.Add(2*time.Second), nil); !changed || err != nil {
		t.Fatalf("completar: %v %v", changed, err)
	}
	if *e.Duration != 2000 {
		t.Errorf("duracion: %d ms", *e.Duration)
	}
	if changed, err := e.Complete(now.Add(time.Hour), nil); changed || err != nil {
		t.Errorf("completar dos veces: %v %v", changed, err)
	}
	if !e.CompletedAt.Equal(now.Add(2 * time.Second)) {
		t.Error("repetir el cierre no debe mover completed_at")
	}
	if _, err := e.Fail(now, FailureExecutor, "x"); !errors.Is(err, ErrExecutionConflict) {
		t.Errorf("fallar una completada: %v", err)
	}
	if _, err := e.Cancel(now); !errors.Is(err, ErrExecutionClosed) {
		t.Errorf("cancelar una completada: %v", err)
	}

	f := running()
	if changed, err := f.Fail(now, FailureTimeout, "t"); !changed || err != nil {
		t.Fatalf("fallar: %v %v", changed, err)
	}
	if changed, err := f.Fail(now, FailureExecutor, "otra"); changed || err != nil || *f.FailureReason != FailureTimeout {
		t.Errorf("fallar dos veces no cambia el motivo: %v %v %s", changed, err, *f.FailureReason)
	}
	if _, err := f.Complete(now, nil); !errors.Is(err, ErrExecutionConflict) {
		t.Errorf("completar una fallida: %v", err)
	}

	c := running()
	if changed, err := c.Cancel(now); !changed || err != nil {
		t.Fatalf("cancelar: %v %v", changed, err)
	}
	for name, step := range map[string]func() (bool, error){
		"completar": func() (bool, error) { return c.Complete(now, nil) },
		"fallar":    func() (bool, error) { return c.Fail(now, FailureExecutor, "x") },
		"cancelar":  func() (bool, error) { return c.Cancel(now) },
	} {
		if changed, err := step(); changed || err != nil || c.Status != StatusCancelled {
			t.Errorf("%s una cancelada: %v %v %s", name, changed, err, c.Status)
		}
	}
}

func TestMotivoDeFalloSaneado(t *testing.T) {
	e := &JobExecution{Status: StatusRunning}
	long := strings.Repeat("ñ", MaxErrorMessageRunes+50) + string(rune(0))
	if _, err := e.Fail(time.Now(), FailureExecutor, "a"+string(rune(0))+"b"+string([]byte{0xff})+long); err != nil {
		t.Fatal(err)
	}
	msg := *e.ErrorMessage
	if strings.ContainsRune(msg, 0) || !utf8.ValidString(msg) {
		t.Fatalf("el motivo debe quedar sin NUL y en UTF-8 valido: %q", msg[:10])
	}
	if n := utf8.RuneCountInString(msg); n != MaxErrorMessageRunes {
		t.Fatalf("el motivo se recorta a %d runas, tiene %d", MaxErrorMessageRunes, n)
	}
}

func TestEsperaCrecienteConTecho(t *testing.T) {
	p := RetryPolicy{BaseDelay: 30 * time.Second, MaxDelay: 5 * time.Minute}
	want := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 5 * time.Minute, 5 * time.Minute}
	for i, w := range want {
		if got := p.Delay(i + 1); got != w {
			t.Errorf("reintento %d: %v, se esperaba %v", i+1, got, w)
		}
	}
	if got := p.Delay(1 << 20); got != p.MaxDelay {
		t.Errorf("un numero enorme de reintentos queda en el techo: %v", got)
	}
	if (RetryPolicy{}).Validate() == nil || (RetryPolicy{BaseDelay: time.Minute, MaxDelay: time.Second}).Validate() == nil {
		t.Error("una politica sin base o con techo menor que la base no es valida")
	}
}

func TestReintentoHeredaElTrabajoYEspera(t *testing.T) {
	now := time.Now().UTC()
	tenant := uuid.New()
	prev := &JobExecution{ID: uuid.New(), JobID: uuid.New(), TenantID: &tenant, Status: StatusFailed, RetryCount: 1}
	next := prev.NextAttempt(uuid.New(), now, time.Minute)
	if next.Status != StatusPending || next.RetryCount != 2 || *next.RetryOf != prev.ID || next.JobID != prev.JobID ||
		*next.TenantID != tenant || !next.NextAttemptAt.Equal(now.Add(time.Minute)) || next.DeadlineAt != nil {
		t.Fatalf("reintento: %+v", next)
	}
	if next.Attempt() != 3 {
		t.Errorf("intento: %d", next.Attempt())
	}
}

func TestResultadoDelEjecutor(t *testing.T) {
	for _, raw := range []string{"", "null", "  null "} {
		if got, err := NormalizeResult(json.RawMessage(raw)); got != nil || err != nil {
			t.Errorf("%q: %v %v", raw, got, err)
		}
	}
	got, err := NormalizeResult(json.RawMessage(` {"rows": 3} `))
	if err != nil || *got != `{"rows": 3}` {
		t.Fatalf("resultado valido: %v %v", got, err)
	}
	nul := `{"a":"` + string([]byte{'\\', 'u', '0', '0', '0', '0'}) + `"}`
	for _, raw := range []string{`{"a":`, nul} {
		if _, err := NormalizeResult(json.RawMessage(raw)); !errors.Is(err, ErrInvalidReport) {
			t.Errorf("%q: %v", raw, err)
		}
	}
}

func TestDefinicionValida(t *testing.T) {
	five, zero := 5, 0
	bad := `{"a":`
	ok := JobDefinition{Name: "Informe", Code: "informe", Handler: "reports.daily", JobType: JobTypeInterval,
		IntervalMinutes: &five, MaxRetries: 1, TimeoutSeconds: 10, Timezone: DefaultTimezone}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(j *JobDefinition){
		"tipo desconocido":      func(j *JobDefinition) { j.JobType = "weekly" },
		"intervalo sin minutos": func(j *JobDefinition) { j.IntervalMinutes = nil },
		"intervalo cero":        func(j *JobDefinition) { j.IntervalMinutes = &zero },
		"reintentos negativos":  func(j *JobDefinition) { j.MaxRetries = -1 },
		"plazo negativo":        func(j *JobDefinition) { j.TimeoutSeconds = -1 },
		"payload ilegible":      func(j *JobDefinition) { j.Payload = &bad },
	}
	for name, mut := range cases {
		j := ok
		mut(&j)
		if err := j.Validate(); !errors.Is(err, ErrInvalidJob) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
