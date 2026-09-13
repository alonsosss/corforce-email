package tenantcell

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/tenantcell/tenantcelltest"
	"go.uber.org/zap"
)

func nuevoResolverDeDominios(url string) (*Resolver, *relojFijo) {
	reloj := &relojFijo{now: time.Unix(1_800_000_000, 0)}
	c := NewDomainResolver(url, tokenDePrueba, zap.NewNop())
	c.now = reloj.Now
	return c, reloj
}

// La celda de un dominio sale del indice de organization, normalizada y con la misma cache que
// la de una empresa: positiva 5 minutos, negativa 30 s.
func TestLaCeldaDeUnDominioSeResuelveYSeCachea(t *testing.T) {
	org, url := tenantcelltest.New(t, tokenDePrueba, nil)
	org.SetDomain("beta.test", "pe-02")
	c, reloj := nuevoResolverDeDominios(url)
	ctx := context.Background()

	for _, raw := range []string{"beta.test", " Beta.TEST "} {
		if cell, err := c.CellOf(ctx, raw); err != nil || cell != "pe-02" {
			t.Fatalf("%q: %q %v", raw, cell, err)
		}
	}
	if org.DomainCalls() != 1 {
		t.Fatalf("consultas: %d; la segunda, con el nombre normalizado, sale de la cache", org.DomainCalls())
	}

	if _, err := c.CellOf(ctx, "nadie.test"); !errors.Is(err, ErrUnknownDomain) {
		t.Fatalf("dominio desconocido: %v", err)
	}
	org.SetDomain("nadie.test", "pe-01")
	if _, err := c.CellOf(ctx, "nadie.test"); !errors.Is(err, ErrUnknownDomain) {
		t.Fatalf("la negativa vale 30 s: %v", err)
	}
	reloj.avanzar(negativeTTL + time.Second)
	if cell, err := c.CellOf(ctx, "nadie.test"); err != nil || cell != "pe-01" {
		t.Fatalf("la negativa caducada se vuelve a preguntar: %q %v", cell, err)
	}
}

// Un nombre que no es un dominio no llega a organization.
func TestUnNombreQueNoEsDominioNoSePregunta(t *testing.T) {
	org, url := tenantcelltest.New(t, tokenDePrueba, nil)
	c, _ := nuevoResolverDeDominios(url)
	for _, raw := range []string{"", "acme", "acme..test", "../tenants/x", "acme.test/cell", "acme.test."} {
		if _, err := c.CellOf(context.Background(), raw); !errors.Is(err, ErrUnknownDomain) {
			t.Fatalf("%q: %v", raw, err)
		}
	}
	if org.Calls() != 0 {
		t.Fatalf("consultas: %d", org.Calls())
	}
}

// Con organization caido vale la ultima celda conocida hasta una hora despues de caducar; sin
// ella, o fuera del margen, no hay celda. Una respuesta de otro contrato no resuelve.
func TestLaCeldaDeUnDominioConOrganizationCaido(t *testing.T) {
	org, url := tenantcelltest.New(t, tokenDePrueba, nil)
	org.SetDomain("beta.test", "pe-02")
	c, reloj := nuevoResolverDeDominios(url)
	ctx := context.Background()
	if _, err := c.CellOf(ctx, "beta.test"); err != nil {
		t.Fatal(err)
	}

	org.SetDown(true)
	if _, err := c.CellOf(ctx, "otro.test"); !errors.Is(err, ErrUnresolved) {
		t.Fatalf("sin respuesta previa: %v", err)
	}
	reloj.avanzar(cacheTTL + time.Minute)
	antes := valor(staleAnswers)
	if cell, err := c.CellOf(ctx, "beta.test"); err != nil || cell != "pe-02" || valor(staleAnswers) != antes+1 {
		t.Fatalf("dentro del margen: %q %v", cell, err)
	}
	reloj.avanzar(staleGrace)
	if _, err := c.CellOf(ctx, "beta.test"); !errors.Is(err, ErrUnresolved) {
		t.Fatalf("fuera del margen: %v", err)
	}

	org.SetDown(false)
	for nombre, raw := range map[string]string{
		"otro dominio en la respuesta": `{"data":{"domain":"otro.test","cell_code":"pe-02"}}`,
		"respuesta de empresa":         `{"data":{"tenant_id":"beta.test","cell_code":"pe-02"}}`,
		"celda mal formada":            `{"data":{"domain":"gamma.test","cell_code":"PE 02"}}`,
	} {
		org.SetRaw(raw)
		if _, err := c.CellOf(ctx, "gamma.test"); !errors.Is(err, ErrUnresolved) {
			t.Fatalf("%s: %v", nombre, err)
		}
	}
}
