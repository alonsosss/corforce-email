package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestDominioDelDestinatario(t *testing.T) {
	cases := map[string]string{
		"Ana@Example.COM":     "example.com",
		" ana@mail.example. ": "mail.example",
		"a@b@c.example":       "c.example",
		"ana@":                "",
		"sin-arroba":          "",
		"ana@localhost":       "",
		"ana@ex ample.com":    "",
		"ana@.example.com":    "",
		"ana@ex..ample.com":   "",
		"Ana <ana@x.com>":     "",
	}
	for in, want := range cases {
		if got := RecipientDomain(in); got != want {
			t.Errorf("%q: %q, se esperaba %q", in, got, want)
		}
	}
}

func TestInstanteDelHecho(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	provider := now.Add(-2 * time.Hour)
	published := now.Add(-time.Minute)
	if got := OccurredAt(provider, published, now); !got.Equal(provider) {
		t.Fatalf("el del proveedor manda: %v", got)
	}
	if got := OccurredAt(time.Time{}, published, now); !got.Equal(published) {
		t.Fatalf("sin el del proveedor, el del sobre: %v", got)
	}
	if got := OccurredAt(now.Add(48*time.Hour), published, now); !got.Equal(published) {
		t.Fatalf("un instante futuro no es creible: %v", got)
	}
	if got := OccurredAt(time.Time{}, time.Time{}, now); !got.Equal(now) {
		t.Fatalf("sin ninguno, el de recepcion: %v", got)
	}
}

func TestClaseDelEvento(t *testing.T) {
	if c, err := EventClass(""); err != nil || c != ClassTransactional {
		t.Fatalf("sin clase es transaccional: %q %v", c, err)
	}
	if c, err := EventClass("marketing"); err != nil || c != ClassMarketing {
		t.Fatalf("marketing: %q %v", c, err)
	}
	if _, err := EventClass("corporate"); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("clase desconocida es evento invalido: %v", err)
	}
}

func TestHitosContables(t *testing.T) {
	for _, a := range []string{"sent", "delivered", "bounced", "complained", "opened", "clicked", "unsubscribed", "failed"} {
		if _, ok := MilestoneFromAction(a); !ok {
			t.Errorf("%s debe contarse", a)
		}
	}
	for _, a := range []string{"queued", "rendering_failure", ""} {
		if _, ok := MilestoneFromAction(a); ok {
			t.Errorf("%s no se cuenta", a)
		}
	}
}

func TestValidacionDelEvento(t *testing.T) {
	ok := newEvent(MilestoneSent, at(1, 1))
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	nil1 := uuid.Nil
	bad := []func(e *MessageEvent){
		func(e *MessageEvent) { e.EventID = uuid.Nil },
		func(e *MessageEvent) { e.TenantID = uuid.Nil },
		func(e *MessageEvent) { e.MessageID = uuid.Nil },
		func(e *MessageEvent) { e.Class = "otro" },
		func(e *MessageEvent) { e.CampaignID = &nil1 },
		func(e *MessageEvent) { e.OccurredAt = time.Time{} },
		func(e *MessageEvent) { e.Milestone = "queued" },
	}
	for i, mutate := range bad {
		e := ok
		mutate(&e)
		if err := e.Validate(); !errors.Is(err, ErrInvalidEvent) {
			t.Errorf("caso %d: se esperaba ErrInvalidEvent, hubo %v", i, err)
		}
	}
}

func campaignEvent(a CampaignAction, t time.Time) CampaignEvent {
	return CampaignEvent{EventID: uuid.New(), TenantID: uuid.New(), CampaignID: uuid.New(), Action: a,
		Status: CampaignStatus("", a), OccurredAt: t}
}

func TestCampanaFueraDeOrden(t *testing.T) {
	started := campaignEvent(CampaignStarted, at(1, 8))
	completed := campaignEvent(CampaignCompleted, at(1, 20))

	c := NewCampaignSeen(completed)
	if !c.Merge(started) {
		t.Fatal("el inicio que llega tarde anade started_at")
	}
	if c.Status != "completed" || !c.StartedAt.Equal(at(1, 8)) || !c.CompletedAt.Equal(at(1, 20)) {
		t.Fatalf("un evento anterior no pisa el estado: %+v", c)
	}
	if c.Merge(started) || c.Merge(completed) {
		t.Fatal("repetir eventos no cambia nada")
	}

	tie := NewCampaignSeen(campaignEvent(CampaignStarted, at(2, 9)))
	tie.Merge(campaignEvent(CampaignCancelled, at(2, 9)))
	if tie.Status != "cancelled" {
		t.Fatalf("a igual instante gana el que cierra: %s", tie.Status)
	}
	tie.Merge(campaignEvent(CampaignStarted, at(2, 9)))
	if tie.Status != "cancelled" {
		t.Fatalf("un inicio del mismo instante no reabre: %s", tie.Status)
	}
}

func TestPausaSoloCambiaElEstado(t *testing.T) {
	c := NewCampaignSeen(campaignEvent(CampaignStarted, at(3, 8)))
	paused := campaignEvent(CampaignPaused, at(3, 9))
	if !c.Merge(paused) || c.Status != "paused" || c.CompletedAt != nil || !c.StartedAt.Equal(at(3, 8)) {
		t.Fatalf("una pausa solo cambia el estado: %+v", c)
	}
	c.Merge(campaignEvent(CampaignResumed, at(3, 10)))
	c.Merge(campaignEvent(CampaignCompleted, at(3, 12)))
	if c.Merge(paused) || c.Status != "completed" || c.CompletedAt == nil {
		t.Fatalf("una pausa que llega tarde no reabre: %+v", c)
	}
	if _, ok := CampaignActionFrom("scheduled"); ok {
		t.Fatal("scheduled no se registra")
	}
}

func TestEstadoDeCampana(t *testing.T) {
	if got := CampaignStatus("running", CampaignStarted); got != "running" {
		t.Fatalf("estado publicado: %s", got)
	}
	for _, bad := range []string{"", "Running", "en curso", "estado_demasiado_largo_para_la_columna"} {
		if got := CampaignStatus(bad, CampaignFailed); got != "failed" {
			t.Errorf("%q: %s, se esperaba la accion", bad, got)
		}
	}
}
