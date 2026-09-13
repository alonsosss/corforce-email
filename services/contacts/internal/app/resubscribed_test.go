package app

import (
	"context"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
)

// La resuscripcion lleva la hora del consentimiento que la justifica, la misma que queda
// en contacts.consents (occurred_at): suppression solo retira las bajas anteriores a ella.
func TestResuscripcionPorFormularioLlevaLaHoraDelConsentimiento(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	c := f.addContact(t, "form@example.com", domain.StatusUnsubscribed, domain.ConsentRevoked)
	f.now = f.now.Add(time.Hour)

	consent, err := f.uc.RecordConsent(ctx, f.tenant, c.ID, ConsentInput{
		Status: "granted", Method: "form", Source: "https://example.com/form", IP: "203.0.113.9",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !consent.OccurredAt.Equal(f.now) || len(f.ev.consentedAt) != 1 || !f.ev.consentedAt[0].Equal(consent.OccurredAt) {
		t.Fatalf("resuscripcion %v, consentimiento %v", f.ev.consentedAt, consent.OccurredAt)
	}

	// Un consentimiento que no reactiva a nadie no publica resuscripcion.
	activo := f.addContact(t, "activo@example.com", domain.StatusActive, domain.ConsentNone)
	if _, err := f.uc.RecordConsent(ctx, f.tenant, activo.ID, ConsentInput{Status: "granted", Method: "api", Source: "crm"}); err != nil {
		t.Fatal(err)
	}
	if len(f.ev.consentedAt) != 1 {
		t.Fatalf("sin resuscripcion nueva: %v", f.ev.consentedAt)
	}
}

func TestResuscripcionPorDobleOptInLlevaLaHoraDelConsentimiento(t *testing.T) {
	f := newFixture(t)
	c := f.addContact(t, "doi@example.com", domain.StatusUnsubscribed, domain.ConsentRevoked)
	confirmed := f.now.Add(72 * time.Hour)
	f.reconsent(t, c, confirmed)

	last := f.s.consents[len(f.s.consents)-1]
	if last.Method != domain.MethodDoubleOptIn || !last.OccurredAt.Equal(confirmed) ||
		len(f.ev.consentedAt) != 1 || !f.ev.consentedAt[0].Equal(last.OccurredAt) {
		t.Fatalf("resuscripcion %v, consentimiento %+v", f.ev.consentedAt, last)
	}
}
