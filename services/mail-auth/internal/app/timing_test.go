package app

import (
	"context"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-auth/internal/domain"
	"github.com/google/uuid"
)

// El tiempo de respuesta a una contrasena incorrecta no puede decir si la direccion existe: un buzon con
// contrasenas de aplicacion no puede tardar mas (ni un buzon sin ninguna menos) que uno inexistente.
func TestElTiempoDeUnaContrasenaMalaNoDistingueBuzones(t *testing.T) {
	const compare = 40 * time.Millisecond
	apps := func(n int) []domain.AppPassword {
		out := make([]domain.AppPassword, n)
		for i := range out {
			out[i] = domain.AppPassword{ID: uuid.New(), Name: "cliente", PasswordHash: "hash:otra-" + string(rune('a'+i))}
		}
		return out
	}
	measure := func(name string, h *harness, service string) time.Duration {
		t.Helper()
		h.verifier.delay = compare
		start := time.Now()
		if got := h.uc.Verify(context.Background(), request("incorrecta", service)); got != domain.ResultBadPassword {
			t.Fatalf("%s: resultado %s", name, got)
		}
		return time.Since(start)
	}
	unknown := measure("inexistente", newHarness(nil), "imap")

	noApps := newHarness(activeMailbox())
	sinApps := measure("sin contrasenas de aplicacion", noApps, "imap")

	manyApps := newHarness(activeMailbox())
	manyApps.repo.appPasswords, manyApps.repo.appProtocol = apps(5), domain.ProtocolIMAP
	conApps := measure("con cinco contrasenas de aplicacion", manyApps, "imap")

	web := newHarness(activeMailbox())
	webmail := measure("webmail", web, "webmail")

	times := map[string]time.Duration{"inexistente": unknown, "sin apps": sinApps, "con apps": conApps, "webmail": webmail}
	for name, d := range times {
		if diff := d - unknown; diff > compare/2 || diff < -compare/2 {
			t.Errorf("%s tarda %v y un buzon inexistente %v: el tiempo distingue las direcciones (todos: %v)", name, d, unknown, times)
		}
	}
}

// La contrasena principal correcta sigue costando una sola comparacion, y la de aplicacion correcta se
// encuentra en cualquier posicion de la lista.
func TestLaSegundaRondaEncuentraCualquierContrasenaDeAplicacion(t *testing.T) {
	h := newHarness(activeMailbox())
	h.repo.appProtocol = domain.ProtocolIMAP
	for _, name := range []string{"uno", "dos", "tres", "cuatro"} {
		h.repo.appPasswords = append(h.repo.appPasswords, domain.AppPassword{ID: uuid.New(), Name: name, PasswordHash: "hash:" + name})
	}
	for _, pw := range []string{"uno", "dos", "tres", "cuatro"} {
		before := len(h.repo.logins)
		if got := h.uc.Verify(context.Background(), request(pw, "imap")); got != domain.ResultOK {
			t.Fatalf("%s: %s", pw, got)
		}
		if l := h.repo.logins[before]; l.AppPasswordID == nil {
			t.Fatalf("%s: el inicio no se atribuyo a su contrasena de aplicacion", pw)
		}
	}
	h.verifier.calls = 0
	if got := h.uc.Verify(context.Background(), request("principal", "imap")); got != domain.ResultOK || h.verifier.calls != 1 {
		t.Fatalf("la principal cuesta una comparacion: %s, %d", got, h.verifier.calls)
	}
}
