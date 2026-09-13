package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/google/uuid"
)

// Una exclusion manual saca al contacto de la audiencia como excluded y no toca su
// consentimiento: no la pidio la persona. Reentregarla no cambia nada y retirarla lo
// devuelve a active con el consentimiento que tenia, sin inventar uno ni resuscribirlo.
func TestExclusionManualNoTocaElConsentimiento(t *testing.T) {
	f := newFixture(t)
	c := f.addContact(t, "manual@example.com", domain.StatusActive, domain.ConsentGranted)
	sin := f.addContact(t, "sin@example.com", domain.StatusActive, domain.ConsentNone)

	for _, x := range []*domain.Contact{c, sin} {
		f.suppressed(x.Email, domain.CauseManual)
		if res := f.apply(t, added(f, x.Email, "manual")); !res.Changed {
			t.Fatalf("%s: la exclusion manual cambia el estado: %+v", x.Email, res)
		}
	}
	if got := f.contact(t, c.ID); got.Status != domain.StatusExcluded || got.ConsentStatus != domain.ConsentGranted || got.Sendable() {
		t.Fatalf("excluido con el consentimiento intacto y fuera de la audiencia: %+v", got)
	}
	if f.ev.count("contact.updated|manual@example.com|status") != 1 {
		t.Fatalf("se publica el cambio de estado: %v", f.ev.events)
	}
	events := len(f.ev.events)
	if res := f.apply(t, added(f, c.Email, "manual")); res.Changed || len(f.ev.events) != events {
		t.Fatalf("la reentrega no cambia nada: %+v %v", res, f.ev.events[events:])
	}

	for _, x := range []*domain.Contact{c, sin} {
		f.suppressed(x.Email)
		if res := f.apply(t, removed(f, x.Email, "manual")); !res.Changed {
			t.Fatalf("%s: retirar la manual lo devuelve: %+v", x.Email, res)
		}
	}
	if got := f.contact(t, c.ID); got.Status != domain.StatusActive || got.ConsentStatus != domain.ConsentGranted || !got.Sendable() {
		t.Fatalf("vuelve a active y a la audiencia con su consentimiento: %+v", got)
	}
	if got := f.contact(t, sin.ID); got.Status != domain.StatusActive || got.ConsentStatus != domain.ConsentNone || got.Sendable() {
		t.Fatalf("vuelve a active sin un consentimiento que nunca dio: %+v", got)
	}
	if len(f.s.consents) != 0 || f.ev.count("consent.") != 0 || f.ev.count("contact.resubscribed") != 0 {
		t.Fatalf("ni revocacion ni concesion ni resuscripcion: %+v %v", f.s.consents, f.ev.events)
	}
}

func TestInvalidPesaMasQueManual(t *testing.T) {
	f := newFixture(t)
	c := f.addContact(t, "inv@example.com", domain.StatusActive, domain.ConsentGranted)
	step := func(ev SuppressionEvent, want domain.Status, causes ...domain.SuppressionCause) {
		t.Helper()
		f.suppressed(c.Email, causes...)
		f.apply(t, ev)
		if got := f.contact(t, c.ID); got.Status != want || got.ConsentStatus != domain.ConsentGranted {
			t.Fatalf("%s %s: %+v, se esperaba %s", ev.Subject, ev.Reason, got, want)
		}
	}
	step(added(f, c.Email, "manual"), domain.StatusExcluded, domain.CauseManual)
	step(added(f, c.Email, "invalid"), domain.StatusInvalid, domain.CauseInvalid, domain.CauseManual)
	step(removed(f, c.Email, "invalid"), domain.StatusExcluded, domain.CauseManual)
	step(removed(f, c.Email, "manual"), domain.StatusActive)
	if len(f.s.consents) != 0 {
		t.Fatalf("ninguna evidencia: %+v", f.s.consents)
	}
}

// Una baja en vigor pesa mas que manual e invalid, y retirarlas no la levanta.
func TestExclusionSobreUnaBaja(t *testing.T) {
	f := newFixture(t)
	c := f.addContact(t, "baja-manual@example.com", domain.StatusUnsubscribed, domain.ConsentRevoked)

	f.suppressed(c.Email, domain.CauseUnsubscribe, domain.CauseInvalid, domain.CauseManual)
	for _, reason := range []string{"manual", "invalid"} {
		if res := f.apply(t, added(f, c.Email, reason)); res.Changed {
			t.Fatalf("%s sobre una baja: %+v", reason, res)
		}
	}
	f.suppressed(c.Email, domain.CauseUnsubscribe)
	f.apply(t, removed(f, c.Email, "invalid"))
	f.apply(t, removed(f, c.Email, "manual"))
	if got := f.contact(t, c.ID); got.Status != domain.StatusUnsubscribed || got.ConsentStatus != domain.ConsentRevoked {
		t.Fatalf("la baja sigue: %+v", got)
	}
}

// La baja que la persona pide estando excluida revoca y pesa mas; al retirar la manual
// queda la baja.
func TestBajaSobreUnExcluido(t *testing.T) {
	f := newFixture(t)
	c := f.addContact(t, "excluido-baja@example.com", domain.StatusActive, domain.ConsentGranted)
	f.suppressed(c.Email, domain.CauseManual)
	f.apply(t, added(f, c.Email, "manual"))

	f.suppressedAt(c.Email, domain.ActiveCause{Cause: domain.CauseUnsubscribe, RegisteredAt: f.now},
		domain.ActiveCause{Cause: domain.CauseManual, RegisteredAt: f.now.Add(-time.Hour)})
	if res := f.apply(t, added(f, c.Email, "unsubscribe")); !res.Changed {
		t.Fatalf("la baja cambia al excluido: %+v", res)
	}
	if got := f.contact(t, c.ID); got.Status != domain.StatusUnsubscribed || got.ConsentStatus != domain.ConsentRevoked {
		t.Fatalf("baja sobre excluido: %+v", got)
	}
	f.suppressed(c.Email, domain.CauseUnsubscribe)
	if res := f.apply(t, removed(f, c.Email, "manual")); res.Changed {
		t.Fatalf("retirar la manual no levanta la baja: %+v", res)
	}
	if f.ev.count("consent.revoked|suppression") != 1 {
		t.Fatalf("una sola revocacion, la de la baja: %v", f.ev.events)
	}
}

// Quien reconsintio y queda excluido antes de que suppression retire su baja antigua:
// cuando esa baja por fin se retira, sigue excluded (la manual sigue vigente).
func TestReconsentimientoConExclusionManualPendiente(t *testing.T) {
	f := newFixture(t)
	baja := f.now
	c := f.addContact(t, "vuelve-excluido@example.com", domain.StatusUnsubscribed, domain.ConsentRevoked)
	f.reconsent(t, c, baja.Add(72*time.Hour))

	f.suppressedAt(c.Email, domain.ActiveCause{Cause: domain.CauseUnsubscribe, RegisteredAt: baja},
		domain.ActiveCause{Cause: domain.CauseManual, RegisteredAt: f.now})
	f.apply(t, added(f, c.Email, "manual"))
	f.apply(t, added(f, c.Email, "unsubscribe"))
	if got := f.contact(t, c.ID); got.Status != domain.StatusExcluded || got.ConsentStatus != domain.ConsentGranted {
		t.Fatalf("excluido, sin revocar el reconsentimiento: %+v", got)
	}
	f.suppressed(c.Email, domain.CauseManual)
	if res := f.apply(t, removed(f, c.Email, "unsubscribe")); res.Changed {
		t.Fatalf("la baja retirada no cambia al excluido: %+v", res)
	}
	f.suppressed(c.Email)
	if res := f.apply(t, removed(f, c.Email, "manual")); !res.Changed || !f.contact(t, c.ID).Sendable() {
		t.Fatalf("sin causas vuelve a la audiencia: %+v", f.contact(t, c.ID))
	}
}

// El doble opt-in no se pide a quien no va a recibir la confirmacion.
func TestNoSePideConfirmacionAUnaDireccionExcluida(t *testing.T) {
	f := newFixture(t)
	for _, st := range []domain.Status{domain.StatusInvalid, domain.StatusExcluded} {
		c := f.addContact(t, string(st)+"@example.com", st, domain.ConsentNone)
		if _, err := f.uc.RequestConfirmation(context.Background(), f.tenant, c.ID, ""); !errors.Is(err, domain.ErrContactNotReachable) {
			t.Fatalf("%s: %v", st, err)
		}
	}
	if len(f.s.tokens) != 0 || len(f.s.consents) != 0 {
		t.Fatalf("ni token ni fila pending: %v %v", f.s.tokens, f.s.consents)
	}
}

// Un contacto excluido no esta en la consulta interna de enviables.
func TestEnviablesExcluyenInvalidYExcluded(t *testing.T) {
	f := newFixture(t)
	ok := f.addContact(t, "ok@example.com", domain.StatusActive, domain.ConsentGranted)
	inv := f.addContact(t, "inv2@example.com", domain.StatusInvalid, domain.ConsentGranted)
	exc := f.addContact(t, "exc2@example.com", domain.StatusExcluded, domain.ConsentGranted)
	got, err := f.uc.SendableContacts(context.Background(), f.tenant, []uuid.UUID{ok.ID, inv.ID, exc.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != ok.ID {
		t.Fatalf("solo el activo con consentimiento: %+v", got)
	}
}
