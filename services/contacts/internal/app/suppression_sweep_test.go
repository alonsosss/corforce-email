package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
)

func (f *fixture) sweep(t *testing.T, only domain.Status) SweepReport {
	t.Helper()
	rep, err := f.uc.SweepSuppression(context.Background(), f.tenant, only)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

// La exclusion manual caduca sin evento: suppression deja de devolverla. El barrido de
// los excluded devuelve a active a quien ya no tiene causa, deja excluded a quien la
// conserva y pasa a invalid a quien solo le queda una direccion no valida.
func TestBarridoDeCaducidad(t *testing.T) {
	f := newFixture(t)
	caducada := f.addContact(t, "caducada@example.com", domain.StatusExcluded, domain.ConsentGranted)
	vigente := f.addContact(t, "vigente@example.com", domain.StatusExcluded, domain.ConsentGranted)
	invalida := f.addContact(t, "invalida@example.com", domain.StatusExcluded, domain.ConsentGranted)
	fuera := f.addContact(t, "fuera@example.com", domain.StatusActive, domain.ConsentGranted)
	f.suppressed(vigente.Email, domain.CauseManual)
	f.suppressed(invalida.Email, domain.CauseInvalid)
	f.suppressed(fuera.Email, domain.CauseManual)

	rep := f.sweep(t, domain.StatusExcluded)
	if rep.Checked != 3 || rep.Changed != 2 {
		t.Fatalf("revisa solo los excluded y cambia los que ya no lo son: %+v", rep)
	}
	want := map[*domain.Contact]domain.Status{
		caducada: domain.StatusActive, vigente: domain.StatusExcluded,
		invalida: domain.StatusInvalid, fuera: domain.StatusActive,
	}
	for c, st := range want {
		if got := f.contact(t, c.ID); got.Status != st || got.ConsentStatus != domain.ConsentGranted {
			t.Errorf("%s: %+v, se esperaba %s", c.Email, got, st)
		}
	}
	if !f.contact(t, caducada.ID).Sendable() {
		t.Fatal("la caducada vuelve a la audiencia con su consentimiento")
	}
	if f.ev.count("contact.updated|") != 2 || len(f.s.consents) != 0 {
		t.Fatalf("un evento por cambio y ninguna evidencia: %v %+v", f.ev.events, f.s.consents)
	}
	if !reflect.DeepEqual(f.sup.batchSizes, []int{3}) {
		t.Fatalf("una consulta en bloque con los excluded: %v", f.sup.batchSizes)
	}

	// Una segunda pasada no cambia nada.
	events := len(f.ev.events)
	if rep := f.sweep(t, domain.StatusExcluded); rep.Changed != 0 || len(f.ev.events) != events {
		t.Fatalf("idempotente: %+v", rep)
	}
}

// El barrido completo corrige lo que no llego por evento (contactos anteriores a los
// estados invalid y excluded, creados con la direccion ya excluida, eventos perdidos) sin
// revocar nunca: una baja vigente sobre un active no se aplica sin su evento.
func TestBarridoCompleto(t *testing.T) {
	f := newFixture(t)
	manual := f.addContact(t, "m@example.com", domain.StatusActive, domain.ConsentGranted)
	invalid := f.addContact(t, "i@example.com", domain.StatusActive, domain.ConsentGranted)
	rebote := f.addContact(t, "r@example.com", domain.StatusActive, domain.ConsentGranted)
	levantado := f.addContact(t, "l@example.com", domain.StatusBounced, domain.ConsentGranted)
	reconsintio := f.addContact(t, "rc@example.com", domain.StatusActive, domain.ConsentGranted)
	baja := f.addContact(t, "b@example.com", domain.StatusUnsubscribed, domain.ConsentRevoked)
	queja := f.addContact(t, "q@example.com", domain.StatusComplained, domain.ConsentGranted)
	f.suppressed(manual.Email, domain.CauseManual)
	f.suppressed(invalid.Email, domain.CauseInvalid, domain.CauseManual)
	f.suppressed(rebote.Email, domain.CauseHardBounce)
	f.suppressed(reconsintio.Email, domain.CauseUnsubscribe)
	f.suppressed(queja.Email, domain.CauseComplaint)

	rep := f.sweep(t, "")
	if rep.Checked != 7 || rep.Changed != 4 {
		t.Fatalf("revisa todos: %+v", rep)
	}
	want := map[*domain.Contact]domain.Status{
		manual: domain.StatusExcluded, invalid: domain.StatusInvalid, rebote: domain.StatusBounced,
		levantado: domain.StatusActive, reconsintio: domain.StatusActive,
		baja: domain.StatusUnsubscribed, queja: domain.StatusComplained,
	}
	for c, st := range want {
		if got := f.contact(t, c.ID); got.Status != st {
			t.Errorf("%s: %s, se esperaba %s", c.Email, got.Status, st)
		}
	}
	if len(f.s.consents) != 0 || f.contact(t, reconsintio.ID).ConsentStatus != domain.ConsentGranted {
		t.Fatalf("el barrido nunca revoca: %+v", f.s.consents)
	}
}

// Lo que se leyo en bloque puede haber cambiado al bloquear la fila: se decide con las
// causas leidas despues del bloqueo, no con la foto.
func TestBarridoNoPisaUnEventoAplicadoEntreMedias(t *testing.T) {
	f := newFixture(t)
	c := f.addContact(t, "carrera@example.com", domain.StatusExcluded, domain.ConsentGranted)
	f.sup.batch = map[string][]domain.ActiveCause{}
	f.suppressed(c.Email, domain.CauseManual)

	if rep := f.sweep(t, domain.StatusExcluded); rep.Checked != 1 || rep.Changed != 0 {
		t.Fatalf("la foto decia caducada, pero la manual se renovo: %+v", rep)
	}
	if got := f.contact(t, c.ID); got.Status != domain.StatusExcluded || len(f.ev.events) != 0 {
		t.Fatalf("sigue excluded sin eventos: %+v %v", got, f.ev.events)
	}
	if f.sup.calls != 1 {
		t.Fatalf("una consulta con la fila bloqueada: %d", f.sup.calls)
	}
}

// El barrido pagina por id y consulta en bloque por pagina.
func TestBarridoPagina(t *testing.T) {
	f := newFixture(t)
	n := suppressionSweepPage + 3
	for i := 0; i < n; i++ {
		f.addContact(t, fmt.Sprintf("p%04d@example.com", i), domain.StatusExcluded, domain.ConsentGranted)
	}
	rep := f.sweep(t, domain.StatusExcluded)
	if rep.Checked != n || rep.Changed != n {
		t.Fatalf("todas las paginas: %+v", rep)
	}
	if !reflect.DeepEqual(f.sup.batchSizes, []int{suppressionSweepPage, 3}) {
		t.Fatalf("una consulta por pagina: %v", f.sup.batchSizes)
	}
	for _, c := range f.s.contacts {
		if c.Status != domain.StatusActive {
			t.Fatalf("%s quedo en %s", c.Email, c.Status)
		}
	}
}

func TestBarridoConSuppressionCaido(t *testing.T) {
	f := newFixture(t)
	c := f.addContact(t, "caido@example.com", domain.StatusExcluded, domain.ConsentGranted)
	f.sup.err = errors.New("timeout")
	if _, err := f.uc.SweepSuppression(context.Background(), f.tenant, domain.StatusExcluded); err == nil {
		t.Fatal("el fallo de suppression se devuelve para reintentar en la siguiente pasada")
	}
	if got := f.contact(t, c.ID); got.Status != domain.StatusExcluded || len(f.ev.events) != 0 {
		t.Fatalf("nada cambia: %+v", got)
	}
}
