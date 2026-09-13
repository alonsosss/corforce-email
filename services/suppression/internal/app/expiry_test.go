package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/suppression/internal/domain"
	"github.com/google/uuid"
)

func (f *fixture) announce(t *testing.T) ExpiryReport {
	t.Helper()
	rep, err := f.uc.AnnounceExpired(context.Background(), f.tenant)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

// in devuelve el instante de la fixture desplazado d.
func (f *fixture) in(d time.Duration) *time.Time {
	at := f.now.Add(d)
	return &at
}

// Una manual caducada se anuncia una sola vez, con las causas que le quedan a la direccion
// y la caducidad que caduco; la que caduca justo ahora tambien, porque la consulta previa
// al envio ya no la devuelve. Una vigente, una sin caducidad, otra causa o la manual de
// otra empresa no se anuncian.
func TestAnuncioDeCaducidad(t *testing.T) {
	f := newFixture()
	f.cause("ana@example.com", domain.ReasonManual, f.in(-time.Minute))
	f.cause("ana@example.com", domain.ReasonUnsubscribe, nil)
	f.cause("justo@example.com", domain.ReasonManual, f.in(0))
	f.cause("luis@example.com", domain.ReasonManual, f.in(time.Hour))
	f.cause("eva@example.com", domain.ReasonManual, nil)
	f.cause("rebote@example.com", domain.ReasonHardBounce, nil)
	otra := &domain.Entry{ID: uuid.New(), TenantID: uuid.New(), Email: "ana@example.com", Reason: domain.ReasonManual, ExpiresAt: f.in(-time.Hour)}
	f.entries.entries = append(f.entries.entries, otra)

	if rep := f.announce(t); rep.Announced != 2 {
		t.Fatalf("dos caducadas: %+v", rep)
	}
	want := []string{
		"expired|ana@example.com|manual|unsubscribe|2026-09-12T11:59:00Z",
		"expired|justo@example.com|manual||2026-09-12T12:00:00Z",
	}
	if !reflect.DeepEqual(f.events.events, want) {
		t.Fatalf("eventos:\n%v\nse esperaba\n%v", f.events.events, want)
	}
	if !reflect.DeepEqual(f.entries.locks, []string{f.tenant.String() + ":ana@example.com", f.tenant.String() + ":justo@example.com"}) {
		t.Fatalf("cada anuncio toma el bloqueo de su direccion: %v", f.entries.locks)
	}
	if _, ok := f.entries.announced[otra.ID]; ok {
		t.Fatal("la pasada de una empresa no toca otra")
	}

	// Otra pasada, o la de otra replica, no repite nada.
	if rep := f.announce(t); rep.Announced != 0 || len(f.events.events) != 2 {
		t.Fatalf("idempotente: %+v %v", rep, f.events.events)
	}
	// Lo anunciado coincide con la consulta previa al envio en el mismo instante.
	got, err := f.uc.Check(context.Background(), f.tenant, []string{"ana@example.com", "justo@example.com"})
	if err != nil || len(got) != 1 || !reflect.DeepEqual(got[0].Reasons, []domain.Reason{domain.ReasonUnsubscribe}) {
		t.Fatalf("check: %+v err=%v", got, err)
	}

	// Llegada su hora, la vigente se anuncia.
	f.now = f.now.Add(2 * time.Hour)
	if rep := f.announce(t); rep.Announced != 1 || f.events.events[2] != "expired|luis@example.com|manual||2026-09-12T13:00:00Z" {
		t.Fatalf("la de luis: %+v %v", rep, f.events.events)
	}
}

// Una manual renovada despues de anunciar su caducidad vuelve a anunciarse cuando caduca la
// nueva; reactivada sin caducidad, ya no caduca.
func TestCaducidadRenovadaSeVuelveAAnunciar(t *testing.T) {
	f := newFixture()
	ctx := context.Background()
	f.cause("ana@example.com", domain.ReasonManual, f.in(-time.Minute))
	f.cause("luis@example.com", domain.ReasonManual, f.in(-time.Minute))
	if rep := f.announce(t); rep.Announced != 2 {
		t.Fatalf("primera caducidad: %+v", rep)
	}

	if _, err := f.uc.CreateManual(ctx, f.tenant, CreateManualInput{Email: "ana@example.com", Reason: domain.ReasonManual, ExpiresAt: f.in(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, added, err := f.uc.Add(ctx, f.tenant, AddInput{Email: "luis@example.com", Reason: domain.ReasonManual, Source: SourceImport}); err != nil || !added {
		t.Fatalf("reactivar sin caducidad: added=%v err=%v", added, err)
	}
	f.events.events = nil
	if rep := f.announce(t); rep.Announced != 0 {
		t.Fatalf("nada caduca antes de la hora nueva: %+v", rep)
	}

	f.now = f.now.Add(2 * time.Hour)
	if rep := f.announce(t); rep.Announced != 1 {
		t.Fatalf("la renovada caduca otra vez: %+v", rep)
	}
	if want := []string{"expired|ana@example.com|manual||2026-09-12T13:00:00Z"}; !reflect.DeepEqual(f.events.events, want) {
		t.Fatalf("se anuncia la caducidad nueva: %v", f.events.events)
	}
}

// Lo que cambio entre la lectura y el reclamo manda: una manual renovada, retirada o ya
// anunciada por otra replica no se anuncia.
func TestReclamoPerdidoNoAnuncia(t *testing.T) {
	f := newFixture()
	renovada := f.cause("r@example.com", domain.ReasonManual, f.in(-time.Minute))
	retirada := f.cause("x@example.com", domain.ReasonManual, f.in(-time.Minute))
	anunciada := f.cause("o@example.com", domain.ReasonManual, f.in(-time.Minute))
	f.entries.beforeClaim = func(id uuid.UUID) {
		switch id {
		case renovada.ID:
			renovada.ExpiresAt = f.in(time.Hour)
		case retirada.ID:
			if err := f.entries.Delete(context.Background(), f.tenant, retirada.ID); err != nil {
				t.Fatal(err)
			}
		case anunciada.ID:
			f.entries.announced = map[uuid.UUID]time.Time{anunciada.ID: *anunciada.ExpiresAt}
		}
	}
	if rep := f.announce(t); rep.Announced != 0 || len(f.events.events) != 0 {
		t.Fatalf("ningun anuncio: %+v %v", rep, f.events.events)
	}
}

// Un fallo al reclamar corta la pasada de la empresa sin anunciar nada; la siguiente pasada
// lo anuncia.
func TestFalloAlReclamarSeRetomaEnLaSiguientePasada(t *testing.T) {
	f := newFixture()
	f.cause("ana@example.com", domain.ReasonManual, f.in(-time.Minute))
	f.entries.claimErr = errors.New("conexion perdida")
	if _, err := f.uc.AnnounceExpired(context.Background(), f.tenant); err == nil {
		t.Fatal("el fallo se devuelve")
	}
	if len(f.events.events) != 0 || len(f.entries.announced) != 0 {
		t.Fatalf("nada anunciado: %v %v", f.events.events, f.entries.announced)
	}
	f.entries.claimErr = nil
	if rep := f.announce(t); rep.Announced != 1 {
		t.Fatalf("la siguiente pasada: %+v", rep)
	}
}

// El recorrido pagina por (expires_at, id): con la misma caducidad en todas, el id
// desempata y ninguna se queda fuera ni se anuncia dos veces.
func TestAnuncioPagina(t *testing.T) {
	f := newFixture()
	n := expiryPage + 3
	for i := 0; i < n; i++ {
		f.cause(fmt.Sprintf("p%04d@example.com", i), domain.ReasonManual, f.in(-time.Minute))
	}
	if rep := f.announce(t); rep.Announced != n || len(f.events.events) != n {
		t.Fatalf("todas: %+v, %d eventos", rep, len(f.events.events))
	}
	seen := map[string]bool{}
	for _, e := range f.events.events {
		if seen[e] {
			t.Fatalf("anunciada dos veces: %s", e)
		}
		seen[e] = true
	}
}
