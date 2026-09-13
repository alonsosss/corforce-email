package tenantcell

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/tenantcell/tenantcelltest"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"go.uber.org/zap"
)

const tokenDePrueba = "token-interno"

// relojFijo es un reloj que solo avanza cuando la prueba lo pide.
type relojFijo struct {
	mu  sync.Mutex
	now time.Time
}

func (r *relojFijo) Now() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.now
}

func (r *relojFijo) avanzar(d time.Duration) {
	r.mu.Lock()
	r.now = r.now.Add(d)
	r.mu.Unlock()
}

func nuevoResolver(url string) (*Resolver, *relojFijo) {
	reloj := &relojFijo{now: time.Unix(1_800_000_000, 0)}
	c := NewResolver(url, tokenDePrueba, zap.NewNop())
	c.now = reloj.Now
	return c, reloj
}

func valor(c prometheus.Counter) float64 {
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		return -1
	}
	return m.GetCounter().GetValue()
}

func TestLaCeldaSeCacheaYCaduca(t *testing.T) {
	tenant := uuid.NewString()
	org, url := tenantcelltest.New(t, tokenDePrueba, map[string]string{tenant: "pe-02"})
	c, reloj := nuevoResolver(url)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if cell, err := c.CellOf(ctx, tenant); err != nil || cell != "pe-02" {
			t.Fatalf("resolucion %d: %q %v", i, cell, err)
		}
	}
	if org.Calls() != 1 {
		t.Fatalf("con la entrada vigente se pregunto %d veces", org.Calls())
	}
	// La forma del identificador no abre otra entrada: la clave es el uuid canonico.
	if cell, err := c.CellOf(ctx, strings.ToUpper(tenant)); err != nil || cell != "pe-02" || org.Calls() != 1 {
		t.Fatalf("mayusculas: %q %v, llamadas %d", cell, err, org.Calls())
	}
	reloj.avanzar(cacheTTL + time.Second)
	if _, err := c.CellOf(ctx, tenant); err != nil || org.Calls() != 2 {
		t.Fatalf("al caducar se vuelve a preguntar: %v, llamadas %d", err, org.Calls())
	}
}

func TestLaURLDeOrganizationAdmiteBarraFinal(t *testing.T) {
	tenant := uuid.NewString()
	_, url := tenantcelltest.New(t, tokenDePrueba, map[string]string{tenant: "pe-01"})
	c, _ := nuevoResolver(url + "/")
	if cell, err := c.CellOf(context.Background(), tenant); err != nil || cell != "pe-01" {
		t.Fatalf("%q %v", cell, err)
	}
}

func TestUnaEmpresaDesconocidaFallaCerradoYSeRecuerdaPoco(t *testing.T) {
	org, url := tenantcelltest.New(t, tokenDePrueba, nil)
	c, reloj := nuevoResolver(url)
	tenant := uuid.NewString()
	for i := 0; i < 2; i++ {
		if cell, err := c.CellOf(context.Background(), tenant); !errors.Is(err, ErrUnknownTenant) || cell != "" {
			t.Fatalf("empresa desconocida: %q %v", cell, err)
		}
	}
	if org.Calls() != 1 {
		t.Fatalf("la respuesta negativa no se cacheo: %d llamadas", org.Calls())
	}
	// Ya existe (alta posterior): la negativa caduca antes que una positiva.
	org.SetCell(tenant, "pe-01")
	reloj.avanzar(negativeTTL + time.Second)
	if cell, err := c.CellOf(context.Background(), tenant); err != nil || cell != "pe-01" {
		t.Fatalf("tras caducar la negativa: %q %v", cell, err)
	}

	// Sin empresa valida no se pregunta a nadie.
	antes := org.Calls()
	for _, id := range []string{"", "no-es-uuid", "../../cells"} {
		if _, err := c.CellOf(context.Background(), id); !errors.Is(err, ErrUnknownTenant) {
			t.Fatalf("empresa %q: %v", id, err)
		}
	}
	if org.Calls() != antes {
		t.Fatalf("se pregunto por una empresa invalida")
	}
}

func TestSinRespuestaDeOrganizationNoHayCelda(t *testing.T) {
	tenant := uuid.NewString()
	org, url := tenantcelltest.New(t, tokenDePrueba, map[string]string{tenant: "pe-02"})
	org.SetDown(true)
	c, _ := nuevoResolver(url)
	for i := 0; i < 2; i++ {
		if cell, err := c.CellOf(context.Background(), tenant); !errors.Is(err, ErrUnresolved) || cell != "" {
			t.Fatalf("organization caido: %q %v", cell, err)
		}
	}
	// Un fallo no se cachea: cada peticion vuelve a intentarlo.
	if org.Calls() != 2 {
		t.Fatalf("llamadas: %d", org.Calls())
	}

	parado := NewResolver("http://127.0.0.1:1", tokenDePrueba, zap.NewNop())
	if _, err := parado.CellOf(context.Background(), tenant); !errors.Is(err, ErrUnresolved) {
		t.Fatalf("organization inalcanzable: %v", err)
	}
}

// Solo la respuesta del contrato cuenta. Un 404 que no es TENANT_NOT_FOUND (una ruta que esta
// version de organization no sirve) no convierte a la empresa en desconocida.
func TestRespuestasFueraDeContratoNoResuelven(t *testing.T) {
	tenant := uuid.NewString()
	for nombre, raw := range map[string]string{
		"otra empresa":          fmt.Sprintf(`{"data":{"tenant_id":%q,"cell_code":"pe-02"}}`, uuid.NewString()),
		"celda en mayusculas":   fmt.Sprintf(`{"data":{"tenant_id":%q,"cell_code":"PE-02"}}`, tenant),
		"celda con ruta":        fmt.Sprintf(`{"data":{"tenant_id":%q,"cell_code":"../pe-02"}}`, tenant),
		"sin celda":             fmt.Sprintf(`{"data":{"tenant_id":%q}}`, tenant),
		"no es json":            `<html>pe-02</html>`,
		"celda demasiado larga": fmt.Sprintf(`{"data":{"tenant_id":%q,"cell_code":%q}}`, tenant, strings.Repeat("a", MaxCodeLen+1)),
	} {
		org, url := tenantcelltest.New(t, tokenDePrueba, nil)
		org.SetRaw(raw)
		c, _ := nuevoResolver(url)
		if cell, err := c.CellOf(context.Background(), tenant); !errors.Is(err, ErrUnresolved) {
			t.Errorf("%s: %q %v", nombre, cell, err)
		}
	}

	_, url := tenantcelltest.New(t, tokenDePrueba, nil)
	c, _ := nuevoResolver(url + "/otra-version")
	if _, err := c.CellOf(context.Background(), tenant); !errors.Is(err, ErrUnresolved) {
		t.Fatalf("404 de ruta: %v, se esperaba ErrUnresolved", err)
	}
}

func TestConOrganizationCaidoValeLaUltimaCeldaUnTiempo(t *testing.T) {
	tenant := uuid.NewString()
	org, url := tenantcelltest.New(t, tokenDePrueba, map[string]string{tenant: "pe-02"})
	c, reloj := nuevoResolver(url)
	if _, err := c.CellOf(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}
	org.SetDown(true)
	antes := valor(staleAnswers)

	reloj.avanzar(cacheTTL + time.Minute)
	if cell, err := c.CellOf(context.Background(), tenant); err != nil || cell != "pe-02" {
		t.Fatalf("dentro del margen: %q %v", cell, err)
	}
	if got := valor(staleAnswers); got != antes+1 {
		t.Fatalf("cell_resolution_stale_total: %v, se esperaba %v", got, antes+1)
	}
	reloj.avanzar(staleGrace)
	if cell, err := c.CellOf(context.Background(), tenant); !errors.Is(err, ErrUnresolved) {
		t.Fatalf("pasado el margen: %q %v", cell, err)
	}
	// Una baja definitiva sustituye a la ultima celda conocida.
	org.SetDown(false)
	org.SetCell(tenant, "")
	if _, err := c.CellOf(context.Background(), tenant); !errors.Is(err, ErrUnknownTenant) {
		t.Fatalf("empresa borrada: %v", err)
	}
}

func TestLaCacheEstaAcotada(t *testing.T) {
	a, b, d := uuid.NewString(), uuid.NewString(), uuid.NewString()
	org, url := tenantcelltest.New(t, tokenDePrueba, map[string]string{a: "pe-01", b: "pe-02", d: "pe-03"})
	c, _ := nuevoResolver(url)
	c.max = 2
	for _, id := range []string{a, b, a, d} {
		if _, err := c.CellOf(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}
	if len(c.entries) != 2 || c.order.Len() != 2 {
		t.Fatalf("entradas: %d/%d", len(c.entries), c.order.Len())
	}
	// Se fue la usada hace mas tiempo (b); a, usada despues, sigue.
	antes := org.Calls()
	if _, err := c.CellOf(context.Background(), a); err != nil || org.Calls() != antes {
		t.Fatalf("a deberia seguir en la cache")
	}
	if _, err := c.CellOf(context.Background(), b); err != nil || org.Calls() != antes+1 {
		t.Fatalf("b deberia haber salido de la cache")
	}
}

func TestPeticionesSimultaneasHacenUnaSolaConsulta(t *testing.T) {
	tenant := uuid.NewString()
	org, url := tenantcelltest.New(t, tokenDePrueba, map[string]string{tenant: "pe-02"})
	release := make(chan struct{})
	org.Hold(release)
	c, _ := nuevoResolver(url)

	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if cell, err := c.CellOf(context.Background(), tenant); err != nil || cell != "pe-02" {
				errs <- fmt.Errorf("%q %v", cell, err)
			}
		}()
	}
	// Un cliente que se va no deja sin respuesta a los demas.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.CellOf(ctx, tenant); !errors.Is(err, ErrUnresolved) {
		t.Errorf("cliente que corta: %v", err)
	}
	for org.Calls() == 0 {
		time.Sleep(time.Millisecond)
	}
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if org.Calls() != 1 {
		t.Fatalf("consultas a organization: %d", org.Calls())
	}
}

func TestCodigoDeCelda(t *testing.T) {
	for code, want := range map[string]bool{
		"pe-01": true, "pe01": true, "a": true, strings.Repeat("a", MaxCodeLen): true,
		"": false, "PE-01": false, "pe_01": false, "-pe": false, "pe-": false, "pe--01": false,
		"../pe-01": false, strings.Repeat("a", MaxCodeLen+1): false,
	} {
		if got := ValidCode(code); got != want {
			t.Errorf("ValidCode(%q) = %v", code, got)
		}
	}
}
