package app

import (
	"context"
	"errors"
	"testing"

	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/google/uuid"
)

// added y removed son eventos del productor actual (llevan reasons); lo que decide es lo
// que suppression tiene vigente al aplicarlos, que el test fija con f.suppressed.
func added(f *fixture, email, reason string) SuppressionEvent {
	return SuppressionEvent{Subject: SubjectSuppressionAdded, TenantID: f.tenant, Email: email, Reason: reason, Source: "ses", HasReasons: true}
}

func removed(f *fixture, email, reason string) SuppressionEvent {
	return SuppressionEvent{Subject: SubjectSuppressionRemoved, TenantID: f.tenant, Email: email, Reason: reason, Source: "api", HasReasons: true}
}

func legacy(ev SuppressionEvent) SuppressionEvent {
	ev.HasReasons = false
	return ev
}

func (f *fixture) suppressed(email string, causes ...domain.SuppressionCause) {
	f.sup.causes[email] = causes
}

func (f *fixture) apply(t *testing.T, ev SuppressionEvent) SuppressionResult {
	t.Helper()
	res, err := f.uc.ApplySuppression(context.Background(), ev)
	if err != nil {
		t.Fatalf("%+v: %v", ev, err)
	}
	return res
}

func TestBajaPorSupresionEsIdempotente(t *testing.T) {
	f := newFixture(t)
	c := f.addContact(t, "ana@example.com", domain.StatusActive, domain.ConsentGranted)
	f.suppressed(c.Email, domain.CauseUnsubscribe)

	if res := f.apply(t, added(f, " ANA@example.com ", "unsubscribe")); !res.Changed {
		t.Fatalf("primera entrega: %+v", res)
	}
	got := f.contact(t, c.ID)
	if got.Status != domain.StatusUnsubscribed || got.ConsentStatus != domain.ConsentRevoked {
		t.Fatalf("baja: %+v", got)
	}
	if len(f.s.consents) != 1 || f.s.consents[0].Method != domain.MethodSuppression || f.s.consents[0].Source != "suppression:ses" {
		t.Fatalf("evidencia de la baja: %+v", f.s.consents)
	}
	events := len(f.ev.events)

	if res := f.apply(t, added(f, "ana@example.com", "unsubscribe")); res.Changed {
		t.Fatalf("reentrega: %+v", res)
	}
	if len(f.s.consents) != 1 || len(f.ev.events) != events {
		t.Fatalf("la reentrega no debe duplicar: consents=%d eventos=%v", len(f.s.consents), f.ev.events)
	}
}

func TestRetirarUnaDeVariasCausasNoReactiva(t *testing.T) {
	f := newFixture(t)
	c := f.addContact(t, "b@example.com", domain.StatusActive, domain.ConsentGranted)

	f.suppressed(c.Email, domain.CauseHardBounce, domain.CauseManual)
	f.apply(t, added(f, c.Email, "hard_bounce"))
	if got := f.contact(t, c.ID); got.Status != domain.StatusBounced || got.ConsentStatus != domain.ConsentGranted {
		t.Fatalf("un rebote no toca el consentimiento: %+v", got)
	}

	// El operador retira el rebote; la exclusion manual sigue bloqueando todo envio.
	f.suppressed(c.Email, domain.CauseManual)
	if res := f.apply(t, removed(f, c.Email, "hard_bounce")); res.Changed {
		t.Fatal("retirar el rebote con otra causa vigente no reactiva")
	}
	if got := f.contact(t, c.ID); got.Status != domain.StatusBounced || got.Sendable() {
		t.Fatalf("sigue fuera de la audiencia: %+v", got)
	}

	// Con la ultima causa retirada vuelve a active con su consentimiento.
	f.suppressed(c.Email)
	if res := f.apply(t, removed(f, c.Email, "manual")); !res.Changed {
		t.Fatal("retirar la ultima causa reactiva")
	}
	if got := f.contact(t, c.ID); got.Status != domain.StatusActive || !got.Sendable() {
		t.Fatalf("reactivado: %+v", got)
	}
}

func TestRetirarLaCausaMasGraveDejaLaSiguiente(t *testing.T) {
	f := newFixture(t)
	c := f.addContact(t, "g@example.com", domain.StatusActive, domain.ConsentGranted)

	f.suppressed(c.Email, domain.CauseComplaint, domain.CauseHardBounce)
	f.apply(t, added(f, c.Email, "hard_bounce"))
	f.apply(t, added(f, c.Email, "complaint"))
	if f.contact(t, c.ID).Status != domain.StatusComplained {
		t.Fatal("una queja pesa mas que un rebote")
	}
	f.suppressed(c.Email, domain.CauseHardBounce)
	if res := f.apply(t, removed(f, c.Email, "complaint")); !res.Changed || f.contact(t, c.ID).Status != domain.StatusBounced {
		t.Fatalf("retirar la queja deja el rebote: %+v", f.contact(t, c.ID))
	}

	// Una baja con el rebote vigente retira el consentimiento sin degradar el rebote; al
	// retirar el rebote queda la baja.
	f.suppressed(c.Email, domain.CauseHardBounce, domain.CauseUnsubscribe)
	f.apply(t, added(f, c.Email, "unsubscribe"))
	if got := f.contact(t, c.ID); got.Status != domain.StatusBounced || got.ConsentStatus != domain.ConsentRevoked {
		t.Fatalf("baja sobre rebote: %+v", got)
	}
	f.suppressed(c.Email, domain.CauseUnsubscribe)
	if res := f.apply(t, removed(f, c.Email, "hard_bounce")); !res.Changed || f.contact(t, c.ID).Status != domain.StatusUnsubscribed {
		t.Fatalf("retirar el rebote con la baja vigente: %+v", f.contact(t, c.ID))
	}
}

func TestRetirarLaUltimaCausaNoDevuelveElConsentimiento(t *testing.T) {
	f := newFixture(t)
	c := f.addContact(t, "r@example.com", domain.StatusBounced, domain.ConsentRevoked)
	u := f.addContact(t, "u@example.com", domain.StatusUnsubscribed, domain.ConsentRevoked)

	if res := f.apply(t, removed(f, c.Email, "hard_bounce")); !res.Changed {
		t.Fatal("sin causas vigentes el rebote se levanta")
	}
	if got := f.contact(t, c.ID); got.Status != domain.StatusActive || got.ConsentStatus != domain.ConsentRevoked || got.Sendable() {
		t.Fatalf("active sin consentimiento, fuera de la audiencia: %+v", got)
	}
	// Una baja solo la levanta un consentimiento nuevo, nunca un removed.
	if res := f.apply(t, removed(f, u.Email, "unsubscribe")); res.Changed {
		t.Fatal("un removed no levanta la baja")
	}
	if got := f.contact(t, u.ID); got.Status != domain.StatusUnsubscribed || got.ConsentStatus != domain.ConsentRevoked {
		t.Fatalf("la baja sigue: %+v", got)
	}
	if len(f.s.consents) != 0 {
		t.Fatalf("un removed no escribe consentimiento: %+v", f.s.consents)
	}
}

func TestEventosDesordenadosNoContradicenASuppression(t *testing.T) {
	f := newFixture(t)

	// Rebote registrado y retirado; el removed llega antes que el added.
	a := f.addContact(t, "a@example.com", domain.StatusActive, domain.ConsentGranted)
	f.suppressed(a.Email)
	f.apply(t, removed(f, a.Email, "hard_bounce"))
	f.apply(t, added(f, a.Email, "hard_bounce"))
	if got := f.contact(t, a.ID); got.Status != domain.StatusActive {
		t.Fatalf("un added atrasado no revive un rebote ya retirado: %+v", got)
	}

	// Queja registrada y retirada con un rebote vigente, en orden inverso.
	b := f.addContact(t, "q@example.com", domain.StatusActive, domain.ConsentGranted)
	f.suppressed(b.Email, domain.CauseHardBounce)
	f.apply(t, removed(f, b.Email, "complaint"))
	f.apply(t, added(f, b.Email, "complaint"))
	if got := f.contact(t, b.ID); got.Status != domain.StatusBounced {
		t.Fatalf("queda el rebote, no la queja retirada: %+v", got)
	}

	// Una baja atrasada que suppression ya retiro porque la persona reconsintio no
	// revoca ese consentimiento.
	c := f.addContact(t, "re@example.com", domain.StatusActive, domain.ConsentGranted)
	f.suppressed(c.Email)
	if res := f.apply(t, added(f, c.Email, "unsubscribe")); res.Changed {
		t.Fatal("la baja ya retirada no cambia al contacto")
	}
	// Mientras suppression aun no retira la baja de quien reconsintio, otro evento de la
	// direccion no lo devuelve a unsubscribed.
	f.suppressed(c.Email, domain.CauseUnsubscribe, domain.CauseManual)
	if res := f.apply(t, added(f, c.Email, "manual")); res.Changed {
		t.Fatal("un evento ajeno a la baja no deshace el reconsentimiento")
	}
	if got := f.contact(t, c.ID); got.Status != domain.StatusActive || got.ConsentStatus != domain.ConsentGranted {
		t.Fatalf("el reconsentimiento sigue: %+v", got)
	}
	if len(f.s.consents) != 0 {
		t.Fatalf("ninguna revocacion: %+v", f.s.consents)
	}
}

func TestProductorSinReasonsAplicaLaCausaDelEvento(t *testing.T) {
	f := newFixture(t)
	c := f.addContact(t, "l@example.com", domain.StatusActive, domain.ConsentGranted)
	// Lo que suppression tuviera vigente no se consulta: el productor anterior guardaba
	// una sola fila por direccion y su evento dice todo.
	f.suppressed(c.Email, domain.CauseManual)

	f.apply(t, legacy(added(f, c.Email, "hard_bounce")))
	if f.contact(t, c.ID).Status != domain.StatusBounced {
		t.Fatal("rebote por la causa del evento")
	}
	f.apply(t, legacy(added(f, c.Email, "unsubscribe")))
	if got := f.contact(t, c.ID); got.Status != domain.StatusBounced || got.ConsentStatus != domain.ConsentRevoked {
		t.Fatalf("una baja no degrada el rebote y revoca: %+v", got)
	}
	if res := f.apply(t, legacy(removed(f, c.Email, "hard_bounce"))); !res.Changed || f.contact(t, c.ID).Status != domain.StatusActive {
		t.Fatalf("retirar el rebote reactiva: %+v", f.contact(t, c.ID))
	}
	for _, ev := range []SuppressionEvent{
		legacy(added(f, c.Email, "manual")),
		legacy(removed(f, c.Email, "unsubscribe")),
	} {
		if res := f.apply(t, ev); !res.Ignored || res.Changed {
			t.Fatalf("%+v: %+v", ev, res)
		}
	}
	if f.sup.calls != 0 {
		t.Fatalf("sin reasons no se consulta a suppression: %d", f.sup.calls)
	}
}

func TestSuppressionNoDisponibleNoCambiaNada(t *testing.T) {
	f := newFixture(t)
	c := f.addContact(t, "d@example.com", domain.StatusBounced, domain.ConsentGranted)
	f.sup.err = errors.New("timeout")

	_, err := f.uc.ApplySuppression(context.Background(), removed(f, c.Email, "hard_bounce"))
	if err == nil || IsInputError(err) {
		t.Fatalf("un fallo de suppression es transitorio: %v", err)
	}
	if f.contact(t, c.ID).Status != domain.StatusBounced || len(f.ev.events) != 0 {
		t.Fatalf("nada cambia: %+v %v", f.contact(t, c.ID), f.ev.events)
	}
}

func TestSupresionQueNoCambiaAlContacto(t *testing.T) {
	f := newFixture(t)
	c := f.addContact(t, "c@example.com", domain.StatusUnsubscribed, domain.ConsentRevoked)
	f.suppressed(c.Email, domain.CauseUnsubscribe, domain.CauseManual, domain.CauseInvalid)

	for _, ev := range []SuppressionEvent{
		added(f, c.Email, "manual"),
		added(f, c.Email, "invalid"),
		removed(f, c.Email, "manual"),
	} {
		if res := f.apply(t, ev); res.Changed {
			t.Fatalf("%+v: %+v", ev, res)
		}
	}
	calls := f.sup.calls
	for _, ev := range []SuppressionEvent{
		{Subject: "suppression.entry.otro", TenantID: f.tenant, Email: c.Email, Reason: "unsubscribe", HasReasons: true},
		added(f, "nadie@example.com", "hard_bounce"),
		{Subject: SubjectSuppressionAdded, TenantID: uuid.New(), Email: c.Email, Reason: "hard_bounce", HasReasons: true},
	} {
		if res := f.apply(t, ev); !res.Ignored || res.Changed {
			t.Fatalf("%+v: %+v", ev, res)
		}
	}
	if f.sup.calls != calls {
		t.Fatal("sin contacto no se consulta a suppression")
	}
	if f.contact(t, c.ID).Status != domain.StatusUnsubscribed || len(f.ev.events) != 0 {
		t.Fatalf("nada debe cambiar: %+v %v", f.contact(t, c.ID), f.ev.events)
	}
	if _, err := f.uc.ApplySuppression(context.Background(), added(f, "no-es-email", "unsubscribe")); !IsInputError(err) {
		t.Fatalf("una direccion invalida es un error definitivo: %v", err)
	}
}
