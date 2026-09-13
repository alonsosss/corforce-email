package app

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/suppression/internal/domain"
)

func at(t time.Time) *time.Time { return &t }

// La resuscripcion solo retira la baja registrada antes del consentimiento que la
// justifica; una posterior o del mismo instante sigue, sin evento.
func TestResubscribeSoloRetiraLaBajaAnteriorAlConsentimiento(t *testing.T) {
	f := newFixture()
	ctx := context.Background()
	const email = "ana@example.com"
	baja := f.cause(email, domain.ReasonUnsubscribe, nil)
	baja.CreatedAt = f.now

	for _, consentedAt := range []time.Time{f.now.Add(-time.Hour), f.now} {
		removed, err := f.uc.Resubscribe(ctx, f.tenant, email, at(consentedAt))
		if err != nil || removed {
			t.Fatalf("consentimiento a %s: removed=%v err=%v", consentedAt, removed, err)
		}
	}
	if len(f.events.events) != 0 || f.entries.find(f.tenant, email, domain.ReasonUnsubscribe) == nil {
		t.Fatalf("la baja sigue y no se publica nada: %v", f.events.events)
	}

	removed, err := f.uc.Resubscribe(ctx, f.tenant, email, at(f.now.Add(time.Microsecond)))
	if err != nil || !removed {
		t.Fatalf("consentimiento posterior: removed=%v err=%v", removed, err)
	}
	if want := []string{"removed|ana@example.com|unsubscribe|"}; !reflect.DeepEqual(f.events.events, want) {
		t.Fatalf("eventos: %v", f.events.events)
	}
}

// Un contacts.contact.resubscribed sin consented_at (productor anterior) levanta la baja
// aunque sea posterior, como antes.
func TestResubscribeSinHoraRetiraComoAntes(t *testing.T) {
	f := newFixture()
	baja := f.cause("ana@example.com", domain.ReasonUnsubscribe, nil)
	baja.CreatedAt = f.now.Add(time.Hour)
	if removed, err := f.uc.Resubscribe(context.Background(), f.tenant, "ana@example.com", nil); err != nil || !removed {
		t.Fatalf("removed=%v err=%v", removed, err)
	}
}

// Una baja repetida se vuelve a registrar: hora de alta nueva, datos del nuevo origen y
// suppression.entry.added otra vez. Un rebote repetido sigue siendo idempotente.
func TestBajaRepetidaSeVuelveARegistrar(t *testing.T) {
	f := newFixture()
	ctx := context.Background()
	const email = "eva@example.com"
	first := f.now

	if _, added, err := f.uc.Add(ctx, f.tenant, AddInput{Email: email, Reason: domain.ReasonUnsubscribe, Source: "transactional"}); err != nil || !added {
		t.Fatalf("primera baja: added=%v err=%v", added, err)
	}
	row := f.entries.find(f.tenant, email, domain.ReasonUnsubscribe)
	id := row.ID

	f.now = first.Add(48 * time.Hour)
	a, added, err := f.uc.Add(ctx, f.tenant, AddInput{Email: email, Reason: domain.ReasonUnsubscribe, Source: "campaign", Detail: "enlace"})
	if err != nil || !added {
		t.Fatalf("baja repetida: added=%v err=%v", added, err)
	}
	row = f.entries.find(f.tenant, email, domain.ReasonUnsubscribe)
	if row.ID != id || !row.CreatedAt.Equal(f.now) || row.Source != "campaign" || row.Detail != "enlace" || len(f.entries.entries) != 1 {
		t.Fatalf("la misma fila con la hora y el origen nuevos: %+v filas=%d", row, len(f.entries.entries))
	}
	if !a.CreatedAt.Equal(f.now) {
		t.Fatalf("la direccion devuelta lleva la hora nueva: %+v", a.Entry)
	}
	got, _ := f.uc.Check(ctx, f.tenant, []string{email})
	if len(got) != 1 || len(got[0].Causes) != 1 || !got[0].Causes[0].CreatedAt.Equal(f.now) {
		t.Fatalf("la consulta previa da la hora nueva: %+v", got)
	}
	want := []string{"added|eva@example.com|unsubscribe|unsubscribe", "added|eva@example.com|unsubscribe|unsubscribe"}
	if !reflect.DeepEqual(f.events.events, want) {
		t.Fatalf("eventos: %v", f.events.events)
	}

	f.events.events = nil
	for i := 0; i < 2; i++ {
		if _, added, err := f.uc.Add(ctx, f.tenant, AddInput{Email: email, Reason: domain.ReasonHardBounce, Source: "ses"}); err != nil || added != (i == 0) {
			t.Fatalf("rebote %d: added=%v err=%v", i+1, added, err)
		}
	}
	if len(f.events.events) != 1 {
		t.Fatalf("un rebote repetido no publica: %v", f.events.events)
	}
}

// El hueco completo: baja, reconsentimiento, y una baja nueva antes de que este servicio
// procese contacts.contact.resubscribed. La baja nueva se vuelve a registrar despues del
// consentimiento, asi que ni la resuscripcion ni su reentrega la retiran.
func TestBajaTrasReconsentirSobreviveALaResuscripcion(t *testing.T) {
	f := newFixture()
	ctx := context.Background()
	const email = "luis@example.com"

	if _, _, err := f.uc.Add(ctx, f.tenant, AddInput{Email: email, Reason: domain.ReasonUnsubscribe, Source: "transactional"}); err != nil {
		t.Fatal(err)
	}
	consentedAt := f.now.Add(72 * time.Hour)
	f.now = consentedAt.Add(time.Hour)
	if _, added, err := f.uc.Add(ctx, f.tenant, AddInput{Email: email, Reason: domain.ReasonUnsubscribe, Source: "transactional"}); err != nil || !added {
		t.Fatalf("baja nueva: added=%v err=%v", added, err)
	}

	for i := 0; i < 2; i++ {
		if removed, err := f.uc.Resubscribe(ctx, f.tenant, email, at(consentedAt)); err != nil || removed {
			t.Fatalf("entrega %d de la resuscripcion: removed=%v err=%v", i+1, removed, err)
		}
	}
	got, _ := f.uc.Check(ctx, f.tenant, []string{email})
	if len(got) != 1 || got[0].Reason != domain.ReasonUnsubscribe || !got[0].Causes[0].CreatedAt.Equal(f.now) {
		t.Fatalf("la baja nueva sigue vigente con su hora: %+v", got)
	}
	if n := len(f.events.events); n != 2 || f.events.events[1] != "added|luis@example.com|unsubscribe|unsubscribe" {
		t.Fatalf("dos altas y ninguna retirada: %v", f.events.events)
	}
}
