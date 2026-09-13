package app

import (
	"context"
	"errors"
	"testing"
	"time"

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

// suppressed fija las causas vigentes sin hora, como las devuelve un suppression anterior
// a causes; suppressedAt, con la hora de alta de cada una.
func (f *fixture) suppressed(email string, causes ...domain.SuppressionCause) {
	active := make([]domain.ActiveCause, len(causes))
	for i, c := range causes {
		active[i] = domain.ActiveCause{Cause: c}
	}
	f.sup.causes[email] = active
}

func (f *fixture) suppressedAt(email string, causes ...domain.ActiveCause) {
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

// reconsent lleva a quien se dio de baja por el doble opt-in completo a la hora at y
// devuelve el contacto ya reactivado.
func (f *fixture) reconsent(t *testing.T, c *domain.Contact, at time.Time) *domain.Contact {
	t.Helper()
	ctx := context.Background()
	f.now = at.Add(-time.Minute)
	if _, err := f.uc.RequestConfirmation(ctx, f.tenant, c.ID, "form"); err != nil {
		t.Fatal(err)
	}
	f.now = at
	if err := f.uc.Confirm(ctx, f.tenant, tokenFromURL(t, f), "203.0.113.7", "Mozilla/5.0"); err != nil {
		t.Fatal(err)
	}
	got := f.contact(t, c.ID)
	if !got.Sendable() || f.ev.count("contact.resubscribed|") != 1 {
		t.Fatalf("reconsentimiento: %+v %v", got, f.ev.events)
	}
	return got
}

// El hueco que cierra la hora de las causas: la persona reconsiente por doble opt-in y,
// antes de que suppression retire la baja al recibir contacts.contact.resubscribed, llega
// (o se reentrega) el alta de esa baja antigua. La baja es anterior al consentimiento: no
// lo revoca ni devuelve al contacto a unsubscribed.
func TestBajaAnteriorAlReconsentimientoNoLoRevoca(t *testing.T) {
	f := newFixture(t)
	baja := f.now
	c := f.addContact(t, "vuelve@example.com", domain.StatusUnsubscribed, domain.ConsentRevoked)
	f.reconsent(t, c, baja.Add(72*time.Hour))
	consents, events := len(f.s.consents), len(f.ev.events)

	f.suppressedAt(c.Email, domain.ActiveCause{Cause: domain.CauseUnsubscribe, RegisteredAt: baja})
	for i := 0; i < 2; i++ {
		if res := f.apply(t, added(f, c.Email, "unsubscribe")); res.Changed {
			t.Fatalf("entrega %d de la baja anterior: %+v", i+1, res)
		}
	}
	if got := f.contact(t, c.ID); got.Status != domain.StatusActive || got.ConsentStatus != domain.ConsentGranted || !got.Sendable() {
		t.Fatalf("el reconsentimiento sigue: %+v", got)
	}
	if len(f.s.consents) != consents || len(f.ev.events) != events {
		t.Fatalf("ninguna revocacion ni evento: %v", f.ev.events[events:])
	}
}

// Una baja registrada despues del reconsentimiento siempre lo revoca, y una reentrega de
// la baja antigua despues no cambia nada.
func TestBajaPosteriorAlReconsentimientoLoRevoca(t *testing.T) {
	f := newFixture(t)
	baja := f.now
	c := f.addContact(t, "otra-vez@example.com", domain.StatusUnsubscribed, domain.ConsentRevoked)
	regrant := baja.Add(72 * time.Hour)
	f.reconsent(t, c, regrant)
	consents := len(f.s.consents)

	nueva := regrant.Add(time.Hour)
	f.now = nueva
	f.suppressedAt(c.Email, domain.ActiveCause{Cause: domain.CauseUnsubscribe, RegisteredAt: nueva})
	if res := f.apply(t, added(f, c.Email, "unsubscribe")); !res.Changed {
		t.Fatalf("la baja nueva revoca: %+v", res)
	}
	got := f.contact(t, c.ID)
	if got.Status != domain.StatusUnsubscribed || got.ConsentStatus != domain.ConsentRevoked || got.Sendable() {
		t.Fatalf("baja nueva: %+v", got)
	}
	if len(f.s.consents) != consents+1 || f.s.consents[consents].Method != domain.MethodSuppression ||
		f.s.consents[consents].Status != domain.ConsentRevoked {
		t.Fatalf("evidencia de la baja nueva: %+v", f.s.consents[consents:])
	}
	if f.ev.count("consent.revoked|suppression") != 1 {
		t.Fatalf("evento de la revocacion: %v", f.ev.events)
	}
}

// Sin un consentimiento posterior que pruebe que lo pidio la persona, la baja revoca como
// antes: tanto sin historial como con un consentimiento declarado por la empresa (api)
// despues de la baja, que CheckGrant habria rechazado si contacts la hubiera conocido.
func TestBajaSinReconsentimientoRevocaComoHoy(t *testing.T) {
	f := newFixture(t)
	baja := f.now

	sin := f.addContact(t, "nuevo@example.com", domain.StatusActive, domain.ConsentNone)
	api := f.addContact(t, "crm@example.com", domain.StatusActive, domain.ConsentNone)
	f.now = baja.Add(time.Hour)
	if err := (fakeConsents{f.s}).Append(context.Background(), &domain.Consent{
		TenantID: f.tenant, ContactID: api.ID, Purpose: domain.PurposeMarketing,
		Status: domain.ConsentGranted, Method: domain.MethodAPI, Source: "crm",
	}); err != nil {
		t.Fatal(err)
	}
	if !f.contact(t, api.ID).Sendable() {
		t.Fatal("el contacto de la empresa tiene consentimiento")
	}

	for _, c := range []*domain.Contact{sin, api} {
		f.suppressedAt(c.Email, domain.ActiveCause{Cause: domain.CauseUnsubscribe, RegisteredAt: baja})
		if res := f.apply(t, added(f, c.Email, "unsubscribe")); !res.Changed {
			t.Fatalf("%s: la baja revoca: %+v", c.Email, res)
		}
		if got := f.contact(t, c.ID); got.Status != domain.StatusUnsubscribed || got.ConsentStatus != domain.ConsentRevoked {
			t.Fatalf("%s: %+v", c.Email, got)
		}
	}
}

// Un suppression que aun no publica causes no da la hora de la baja: se aplica la regla
// de antes y la baja revoca aunque haya un reconsentimiento.
func TestProductorSinHorasRevocaComoHoy(t *testing.T) {
	f := newFixture(t)
	c := f.addContact(t, "replica@example.com", domain.StatusUnsubscribed, domain.ConsentRevoked)
	f.reconsent(t, c, f.now.Add(72*time.Hour))

	f.suppressed(c.Email, domain.CauseUnsubscribe)
	if res := f.apply(t, added(f, c.Email, "unsubscribe")); !res.Changed {
		t.Fatalf("sin hora la baja revoca: %+v", res)
	}
	if got := f.contact(t, c.ID); got.Status != domain.StatusUnsubscribed || got.ConsentStatus != domain.ConsentRevoked {
		t.Fatalf("como hoy: %+v", got)
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
