package app

import (
	"context"
	"errors"
	"testing"

	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
)

// expired es suppression.entry.expired del productor actual: la manual caducada, con
// reasons. Como en added y removed, lo que decide es lo que suppression tiene vigente.
func expired(f *fixture, email string) SuppressionEvent {
	return SuppressionEvent{Subject: SubjectSuppressionExpired, TenantID: f.tenant, Email: email, Reason: "manual", Source: "api", HasReasons: true}
}

// La caducidad se aplica como la retirada: sin causa vigente, el excluded vuelve a active
// con el consentimiento que tenia, sin evidencia; la reentrega no cambia nada.
func TestCaducidadDevuelveAlContactoASuEstadoAnterior(t *testing.T) {
	f := newFixture(t)
	c := f.addContact(t, "ana@example.com", domain.StatusExcluded, domain.ConsentGranted)
	f.suppressed(c.Email)

	if res := f.apply(t, expired(f, " ANA@example.com ")); !res.Changed {
		t.Fatalf("primera entrega: %+v", res)
	}
	got := f.contact(t, c.ID)
	if got.Status != domain.StatusActive || got.ConsentStatus != domain.ConsentGranted || !got.Sendable() {
		t.Fatalf("vuelve a active con su consentimiento: %+v", got)
	}
	if f.ev.count("contact.updated|ana@example.com|status") != 1 {
		t.Fatalf("un contacts.contact.updated: %v", f.ev.events)
	}

	if res := f.apply(t, expired(f, c.Email)); res.Changed {
		t.Fatalf("la reentrega no cambia nada: %+v", res)
	}
	if len(f.ev.events) != 1 || len(f.s.consents) != 0 {
		t.Fatalf("ni eventos nuevos ni evidencia: %v %+v", f.ev.events, f.s.consents)
	}
	if f.sup.calls != 2 {
		t.Fatalf("cada entrega consulta suppression con la fila bloqueada: %d", f.sup.calls)
	}
}

// Con otra causa vigente queda el estado de esa causa. unsubscribed solo lo levanta un
// consentimiento nuevo, y una baja pendiente de retirar tras un reconsentimiento no cuenta:
// al caducar la manual, quien reconsintio vuelve a active sin revocar nada.
func TestCaducidadConOtraCausaVigente(t *testing.T) {
	f := newFixture(t)
	invalida := f.addContact(t, "i@example.com", domain.StatusExcluded, domain.ConsentGranted)
	baja := f.addContact(t, "b@example.com", domain.StatusUnsubscribed, domain.ConsentRevoked)
	reconsintio := f.addContact(t, "rc@example.com", domain.StatusExcluded, domain.ConsentGranted)
	f.suppressed(invalida.Email, domain.CauseInvalid)
	f.suppressed(baja.Email)
	f.suppressed(reconsintio.Email, domain.CauseUnsubscribe)

	for _, c := range []*domain.Contact{invalida, baja, reconsintio} {
		f.apply(t, expired(f, c.Email))
	}
	want := map[*domain.Contact]struct {
		st domain.Status
		cs domain.ConsentStatus
	}{
		invalida:    {domain.StatusInvalid, domain.ConsentGranted},
		baja:        {domain.StatusUnsubscribed, domain.ConsentRevoked},
		reconsintio: {domain.StatusActive, domain.ConsentGranted},
	}
	for c, w := range want {
		if got := f.contact(t, c.ID); got.Status != w.st || got.ConsentStatus != w.cs {
			t.Errorf("%s: %s/%s, se esperaba %s/%s", c.Email, got.Status, got.ConsentStatus, w.st, w.cs)
		}
	}
	if len(f.s.consents) != 0 {
		t.Fatalf("la caducidad nunca escribe evidencia: %+v", f.s.consents)
	}
}

// Una caducidad que se aplica tarde no pisa una causa mas nueva: la manual renovada, o una
// queja registrada despues, siguen en suppression y mandan.
func TestCaducidadAtrasadaNoPisaUnaCausaMasNueva(t *testing.T) {
	f := newFixture(t)
	renovada := f.addContact(t, "r@example.com", domain.StatusExcluded, domain.ConsentGranted)
	queja := f.addContact(t, "q@example.com", domain.StatusExcluded, domain.ConsentGranted)
	f.suppressed(renovada.Email, domain.CauseManual)
	f.suppressed(queja.Email, domain.CauseComplaint)

	if res := f.apply(t, expired(f, renovada.Email)); res.Changed {
		t.Fatal("la manual renovada sigue vigente: nada cambia")
	}
	f.apply(t, expired(f, queja.Email))
	if got := f.contact(t, renovada.ID); got.Status != domain.StatusExcluded {
		t.Fatalf("renovada: %+v", got)
	}
	if got := f.contact(t, queja.ID); got.Status != domain.StatusComplained || got.ConsentStatus != domain.ConsentGranted {
		t.Fatalf("la queja posterior manda y no revoca: %+v", got)
	}
}

// Alta, retirada y caducidad de la misma direccion llegan por durables distintos y en
// cualquier orden: el estado final es el que suppression tiene vigente al aplicar el
// ultimo, sea libre (la manual caduco) o excluida (se volvio a excluir).
func TestAltaRetiradaYCaducidadEnCualquierOrden(t *testing.T) {
	orders := [][]string{
		{"added", "removed", "expired"}, {"added", "expired", "removed"},
		{"removed", "added", "expired"}, {"removed", "expired", "added"},
		{"expired", "added", "removed"}, {"expired", "removed", "added"},
	}
	for _, final := range []struct {
		causes []domain.SuppressionCause
		want   domain.Status
	}{
		{nil, domain.StatusActive},
		{[]domain.SuppressionCause{domain.CauseManual}, domain.StatusExcluded},
	} {
		for _, order := range orders {
			f := newFixture(t)
			c := f.addContact(t, "x@example.com", domain.StatusActive, domain.ConsentGranted)
			f.suppressed(c.Email, final.causes...)
			for _, subject := range order {
				ev := expired(f, c.Email)
				switch subject {
				case "added":
					ev = added(f, c.Email, "manual")
				case "removed":
					ev = removed(f, c.Email, "manual")
				}
				f.apply(t, ev)
			}
			if got := f.contact(t, c.ID); got.Status != final.want || got.ConsentStatus != domain.ConsentGranted {
				t.Errorf("%v con %v vigente: %s/%s, se esperaba %s", order, final.causes, got.Status, got.ConsentStatus, final.want)
			}
		}
	}
}

// Sin reasons (un productor que no las publique) la caducidad levanta como la retirada:
// solo al estado de su causa, nunca una baja.
func TestCaducidadSinReasonsLevantaComoLaRetirada(t *testing.T) {
	f := newFixture(t)
	excluido := f.addContact(t, "e@example.com", domain.StatusExcluded, domain.ConsentGranted)
	rebote := f.addContact(t, "r@example.com", domain.StatusBounced, domain.ConsentGranted)

	if res := f.apply(t, legacy(expired(f, excluido.Email))); !res.Changed {
		t.Fatal("levanta la exclusion manual")
	}
	if res := f.apply(t, legacy(expired(f, rebote.Email))); res.Changed {
		t.Fatal("no levanta un estado que no es el de su causa")
	}
	baja := legacy(expired(f, rebote.Email))
	baja.Reason = "unsubscribe"
	if res := f.apply(t, baja); !res.Ignored {
		t.Fatal("una baja solo la levanta un consentimiento nuevo")
	}
	if got := f.contact(t, excluido.ID); got.Status != domain.StatusActive {
		t.Fatalf("excluido: %+v", got)
	}
	if got := f.contact(t, rebote.ID); got.Status != domain.StatusBounced {
		t.Fatalf("rebote: %+v", got)
	}
	if f.sup.calls != 0 {
		t.Fatalf("sin reasons no se consulta suppression: %d", f.sup.calls)
	}
}

// Sin contacto con esa direccion no hay nada que hacer; con suppression caido el evento
// falla sin cambiar nada y JetStream lo reentrega.
func TestCaducidadSinContactoOSinSuppression(t *testing.T) {
	f := newFixture(t)
	if res := f.apply(t, expired(f, "nadie@example.com")); !res.Ignored || f.sup.calls != 0 {
		t.Fatalf("sin contacto: %+v, %d consultas", res, f.sup.calls)
	}
	c := f.addContact(t, "caido@example.com", domain.StatusExcluded, domain.ConsentGranted)
	f.sup.err = errors.New("timeout")
	if _, err := f.uc.ApplySuppression(context.Background(), expired(f, c.Email)); err == nil {
		t.Fatal("el fallo se devuelve para que el evento quede sin confirmar")
	}
	if got := f.contact(t, c.ID); got.Status != domain.StatusExcluded || len(f.ev.events) != 0 {
		t.Fatalf("nada cambia: %+v %v", got, f.ev.events)
	}
}
