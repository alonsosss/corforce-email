package app

import (
	"context"
	"testing"

	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/google/uuid"
)

func added(f *fixture, email, reason string) SuppressionEvent {
	return SuppressionEvent{Subject: SubjectSuppressionAdded, TenantID: f.tenant, Email: email, Reason: reason, Source: "ses"}
}

func removed(f *fixture, email, reason string) SuppressionEvent {
	return SuppressionEvent{Subject: SubjectSuppressionRemoved, TenantID: f.tenant, Email: email, Reason: reason, Source: "api"}
}

func TestBajaPorSupresionEsIdempotente(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	c := f.addContact(t, "ana@example.com", domain.StatusActive, domain.ConsentGranted)

	res, err := f.uc.ApplySuppression(ctx, added(f, " ANA@example.com ", "unsubscribe"))
	if err != nil || !res.Changed {
		t.Fatalf("primera entrega: %+v %v", res, err)
	}
	got := f.contact(t, c.ID)
	if got.Status != domain.StatusUnsubscribed || got.ConsentStatus != domain.ConsentRevoked {
		t.Fatalf("baja: %+v", got)
	}
	if len(f.s.consents) != 1 || f.s.consents[0].Method != domain.MethodSuppression || f.s.consents[0].Source != "suppression:ses" {
		t.Fatalf("evidencia de la baja: %+v", f.s.consents)
	}
	events := len(f.ev.events)

	// Reentrega: nada cambia, ninguna fila ni evento nuevo.
	res, err = f.uc.ApplySuppression(ctx, added(f, "ana@example.com", "unsubscribe"))
	if err != nil || res.Changed {
		t.Fatalf("reentrega: %+v %v", res, err)
	}
	if len(f.s.consents) != 1 || len(f.ev.events) != events {
		t.Fatalf("la reentrega no debe duplicar: consents=%d eventos=%v", len(f.s.consents), f.ev.events)
	}
}

func TestSupresionRespetaLaGravedad(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	c := f.addContact(t, "b@example.com", domain.StatusActive, domain.ConsentGranted)

	if _, err := f.uc.ApplySuppression(ctx, added(f, c.Email, "hard_bounce")); err != nil {
		t.Fatal(err)
	}
	if got := f.contact(t, c.ID); got.Status != domain.StatusBounced || got.ConsentStatus != domain.ConsentGranted {
		t.Fatalf("un rebote no toca el consentimiento: %+v", got)
	}
	// Una baja posterior no degrada el rebote, pero si retira el consentimiento.
	if _, err := f.uc.ApplySuppression(ctx, added(f, c.Email, "unsubscribe")); err != nil {
		t.Fatal(err)
	}
	if got := f.contact(t, c.ID); got.Status != domain.StatusBounced || got.ConsentStatus != domain.ConsentRevoked {
		t.Fatalf("baja sobre rebote: %+v", got)
	}
	if _, err := f.uc.ApplySuppression(ctx, added(f, c.Email, "complaint")); err != nil {
		t.Fatal(err)
	}
	if f.contact(t, c.ID).Status != domain.StatusComplained {
		t.Fatal("una queja pesa mas que un rebote")
	}
	// Retirar el rebote no levanta la queja; retirar la queja si reactiva.
	if res, _ := f.uc.ApplySuppression(ctx, removed(f, c.Email, "hard_bounce")); res.Changed {
		t.Fatal("retirar un rebote no levanta una queja")
	}
	if res, _ := f.uc.ApplySuppression(ctx, removed(f, c.Email, "complaint")); !res.Changed || f.contact(t, c.ID).Status != domain.StatusActive {
		t.Fatalf("retirar la queja: %+v", f.contact(t, c.ID))
	}
	if f.contact(t, c.ID).ConsentStatus != domain.ConsentRevoked {
		t.Fatal("reactivar la direccion no devuelve el consentimiento")
	}
}

func TestSupresionQueNoCambiaAlContacto(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	c := f.addContact(t, "c@example.com", domain.StatusUnsubscribed, domain.ConsentRevoked)

	for _, ev := range []SuppressionEvent{
		added(f, c.Email, "manual"),
		added(f, c.Email, "invalid"),
		removed(f, c.Email, "unsubscribe"),
		{Subject: "suppression.entry.otro", TenantID: f.tenant, Email: c.Email, Reason: "unsubscribe"},
		added(f, "nadie@example.com", "hard_bounce"),
		{Subject: SubjectSuppressionAdded, TenantID: uuid.New(), Email: c.Email, Reason: "hard_bounce"},
	} {
		res, err := f.uc.ApplySuppression(ctx, ev)
		if err != nil || !res.Ignored || res.Changed {
			t.Fatalf("%+v: %+v %v", ev, res, err)
		}
	}
	if f.contact(t, c.ID).Status != domain.StatusUnsubscribed || len(f.ev.events) != 0 {
		t.Fatalf("nada debe cambiar: %+v %v", f.contact(t, c.ID), f.ev.events)
	}
	if _, err := f.uc.ApplySuppression(ctx, added(f, "no-es-email", "unsubscribe")); !IsInputError(err) {
		t.Fatalf("una direccion invalida es un error definitivo: %v", err)
	}
}
