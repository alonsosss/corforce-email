package domain

import (
	"errors"
	"testing"
)

func spec(name string, scopes ...HandlerScope) HandlerSpec {
	return HandlerSpec{Name: name, Service: "reports", MaxTimeoutSeconds: 600, Scopes: scopes}
}

func TestCatalogoValidoResuelvePorTipoDeTrabajo(t *testing.T) {
	c, err := NewHandlerCatalog([]HandlerSpec{
		spec("reports.daily", ScopeTenant),
		spec("ops.cleanup", ScopePlatform),
		spec("reports.both", ScopeTenant, ScopePlatform),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Resolve("reports.daily", false); err != nil {
		t.Errorf("de empresa con scope tenant: %v", err)
	}
	if _, err := c.Resolve("reports.daily", true); !errors.Is(err, ErrHandlerNotAllowed) {
		t.Errorf("de plataforma con scope solo tenant: %v", err)
	}
	if _, err := c.Resolve("ops.cleanup", false); !errors.Is(err, ErrHandlerNotAllowed) {
		t.Errorf("de empresa con scope solo platform: %v", err)
	}
	if _, err := c.Resolve("no.existe", false); !errors.Is(err, ErrHandlerNotAllowed) {
		t.Errorf("desconocido: %v", err)
	}
	list := c.List()
	if len(list) != 3 || list[0].Name != "ops.cleanup" || list[2].Name != "reports.daily" {
		t.Fatalf("listado ordenado por nombre: %+v", list)
	}
	list[0].Scopes[0] = ScopeTenant
	if c.List()[0].Scopes[0] != ScopePlatform {
		t.Fatal("List no debe exponer el estado interno del catalogo")
	}
}

func TestCatalogoIncoherenteNoArranca(t *testing.T) {
	cases := map[string][]HandlerSpec{
		"nombre repetido":      {spec("a.b", ScopeTenant), spec("a.b", ScopePlatform)},
		"nombre invalido":      {spec("A B", ScopeTenant)},
		"servicio invalido":    {{Name: "a.b", Service: "Reports", MaxTimeoutSeconds: 60, Scopes: []HandlerScope{ScopeTenant}}},
		"plazo cero":           {{Name: "a.b", Service: "r", MaxTimeoutSeconds: 0, Scopes: []HandlerScope{ScopeTenant}}},
		"plazo sobre el techo": {{Name: "a.b", Service: "r", MaxTimeoutSeconds: MaxHandlerTimeoutSeconds + 1, Scopes: []HandlerScope{ScopeTenant}}},
		"sin scopes":           {spec("a.b")},
		"scope desconocido":    {spec("a.b", HandlerScope("global"))},
		"scope repetido":       {spec("a.b", ScopeTenant, ScopeTenant)},
	}
	for name, specs := range cases {
		if _, err := NewHandlerCatalog(specs); err == nil {
			t.Errorf("%s: se esperaba error", name)
		}
	}
}

func TestCatalogoVacioNoPermiteNada(t *testing.T) {
	var nilCatalog *HandlerCatalog
	if _, err := nilCatalog.Resolve("x", false); !errors.Is(err, ErrHandlerNotAllowed) {
		t.Fatalf("catalogo nil: %v", err)
	}
	if len(nilCatalog.List()) != 0 {
		t.Fatal("catalogo nil sin manejadores")
	}
	empty, err := NewHandlerCatalog(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := empty.Resolve("x", false); !errors.Is(err, ErrHandlerNotAllowed) {
		t.Fatalf("catalogo vacio: %v", err)
	}
}

func TestPlazoEfectivoAcotadoPorElManejador(t *testing.T) {
	s := spec("a.b", ScopeTenant)
	for job, want := range map[int]int{0: 600, -5: 600, 120: 120, 600: 600, 5000: 600} {
		if got := s.EffectiveTimeoutSeconds(job); got != want {
			t.Errorf("trabajo con %d s: %d, se esperaba %d", job, got, want)
		}
	}
}
