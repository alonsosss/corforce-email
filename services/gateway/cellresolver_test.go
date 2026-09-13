package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

// organizationFake hace de API interna de organization: sirve la celda de las empresas que
// conoce, 404 TENANT_NOT_FOUND para las demas y, apagada, 500. Comprueba que el gateway llega
// con el token interno y sin usuario (RequireInternalCaller).
type organizationFake struct {
	t *testing.T

	mu      sync.Mutex
	cells   map[string]string
	down    bool
	raw     string // si no esta vacia, respuesta 200 literal
	calls   int
	release chan struct{} // si no es nil, cada respuesta espera a que se cierre
}

func newOrganizationFake(t *testing.T, cells map[string]string) (*organizationFake, *httptest.Server) {
	t.Helper()
	f := &organizationFake{t: t, cells: cells}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *organizationFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.calls++
	down, raw, release := f.down, f.raw, f.release
	f.mu.Unlock()
	if release != nil {
		<-release
	}
	if r.Header.Get("X-Gateway-Token") != "token-interno" || r.Header.Get("X-User-ID") != "" {
		f.t.Errorf("organization recibio token %q y usuario %q", r.Header.Get("X-Gateway-Token"), r.Header.Get("X-User-ID"))
	}
	tenant, ok := strings.CutPrefix(r.URL.Path, "/internal/organization/tenants/")
	tenant, ok2 := strings.CutSuffix(tenant, "/cell")
	w.Header().Set("Content-Type", "application/json")
	switch {
	case !ok || !ok2:
		http.NotFound(w, r)
	case down:
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"code":"INTERNAL_ERROR","message":"x"}}`))
	case raw != "":
		_, _ = w.Write([]byte(raw))
	default:
		f.mu.Lock()
		cell, known := f.cells[tenant]
		f.mu.Unlock()
		if !known {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"TENANT_NOT_FOUND","message":"empresa no encontrada"}}`))
			return
		}
		_, _ = fmt.Fprintf(w, `{"data":{"tenant_id":%q,"cell_code":%q}}`, tenant, cell)
	}
}

func (f *organizationFake) set(fn func(f *organizationFake)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *organizationFake) llamadas() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// relojFijo es un reloj que solo avanza cuando el test lo pide.
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

func nuevoResolver(url string) (*cellResolver, *relojFijo) {
	reloj := &relojFijo{now: time.Unix(1_800_000_000, 0)}
	c := newCellResolver(url, "token-interno", zap.NewNop())
	c.now = reloj.Now
	return c, reloj
}

func counterValue(name string, labels map[string]string) float64 {
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		return -1
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
	metric:
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if want, ok := labels[l.GetName()]; ok && want != l.GetValue() {
					continue metric
				}
			}
			return m.GetCounter().GetValue()
		}
	}
	return 0
}

func TestLaCeldaSeCacheaYCaduca(t *testing.T) {
	tenant := uuid.NewString()
	org, srv := newOrganizationFake(t, map[string]string{tenant: "pe-02"})
	c, reloj := nuevoResolver(srv.URL)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if cell, err := c.cellOf(ctx, tenant); err != nil || cell != "pe-02" {
			t.Fatalf("resolucion %d: %q %v", i, cell, err)
		}
	}
	if org.llamadas() != 1 {
		t.Fatalf("con la entrada vigente se pregunto %d veces", org.llamadas())
	}
	// La forma del identificador no abre otra entrada: la clave es el uuid canonico.
	if cell, err := c.cellOf(ctx, strings.ToUpper(tenant)); err != nil || cell != "pe-02" || org.llamadas() != 1 {
		t.Fatalf("mayusculas: %q %v, llamadas %d", cell, err, org.llamadas())
	}
	reloj.avanzar(cellCacheTTL + time.Second)
	if _, err := c.cellOf(ctx, tenant); err != nil || org.llamadas() != 2 {
		t.Fatalf("al caducar se vuelve a preguntar: %v, llamadas %d", err, org.llamadas())
	}
}

func TestUnaEmpresaDesconocidaFallaCerradoYSeRecuerdaPoco(t *testing.T) {
	org, srv := newOrganizationFake(t, map[string]string{})
	c, reloj := nuevoResolver(srv.URL)
	tenant := uuid.NewString()
	for i := 0; i < 2; i++ {
		if cell, err := c.cellOf(context.Background(), tenant); !errors.Is(err, errUnknownTenant) || cell != "" {
			t.Fatalf("empresa desconocida: %q %v", cell, err)
		}
	}
	if org.llamadas() != 1 {
		t.Fatalf("la respuesta negativa no se cacheo: %d llamadas", org.llamadas())
	}
	// Ya existe (alta posterior): la negativa caduca antes que una positiva.
	org.set(func(f *organizationFake) { f.cells[tenant] = "pe-01" })
	reloj.avanzar(cellNegativeTTL + time.Second)
	if cell, err := c.cellOf(context.Background(), tenant); err != nil || cell != "pe-01" {
		t.Fatalf("tras caducar la negativa: %q %v", cell, err)
	}

	// Sin empresa valida en la sesion no se pregunta a nadie.
	antes := org.llamadas()
	for _, id := range []string{"", "no-es-uuid", "../../cells"} {
		if _, err := c.cellOf(context.Background(), id); !errors.Is(err, errUnknownTenant) {
			t.Fatalf("empresa %q: %v", id, err)
		}
	}
	if org.llamadas() != antes {
		t.Fatalf("se pregunto por una empresa invalida")
	}
}

func TestSinRespuestaDeOrganizationNoHayCelda(t *testing.T) {
	tenant := uuid.NewString()
	org, srv := newOrganizationFake(t, map[string]string{tenant: "pe-02"})
	org.set(func(f *organizationFake) { f.down = true })
	c, _ := nuevoResolver(srv.URL)
	for i := 0; i < 2; i++ {
		if cell, err := c.cellOf(context.Background(), tenant); !errors.Is(err, errCellUnresolved) || cell != "" {
			t.Fatalf("organization caido: %q %v", cell, err)
		}
	}
	// Un fallo no se cachea: cada peticion vuelve a intentarlo.
	if org.llamadas() != 2 {
		t.Fatalf("llamadas: %d", org.llamadas())
	}

	parado := newCellResolver("http://127.0.0.1:1", "token-interno", zap.NewNop())
	if _, err := parado.cellOf(context.Background(), tenant); !errors.Is(err, errCellUnresolved) {
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
		"celda demasiado larga": fmt.Sprintf(`{"data":{"tenant_id":%q,"cell_code":%q}}`, tenant, strings.Repeat("a", maxCellCodeLen+1)),
	} {
		org, srv := newOrganizationFake(t, nil)
		org.set(func(f *organizationFake) { f.raw = raw })
		c, _ := nuevoResolver(srv.URL)
		if cell, err := c.cellOf(context.Background(), tenant); !errors.Is(err, errCellUnresolved) {
			t.Errorf("%s: %q %v", nombre, cell, err)
		}
	}

	_, srv := newOrganizationFake(t, nil)
	c, _ := nuevoResolver(srv.URL + "/otra-version")
	if _, err := c.cellOf(context.Background(), tenant); !errors.Is(err, errCellUnresolved) {
		t.Fatalf("404 de ruta: %v, se esperaba errCellUnresolved", err)
	}
}

func TestConOrganizationCaidoValeLaUltimaCeldaUnTiempo(t *testing.T) {
	tenant := uuid.NewString()
	org, srv := newOrganizationFake(t, map[string]string{tenant: "pe-02"})
	c, reloj := nuevoResolver(srv.URL)
	if _, err := c.cellOf(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}
	org.set(func(f *organizationFake) { f.down = true })
	antes := counterValue("cell_resolution_stale_total", nil)

	reloj.avanzar(cellCacheTTL + time.Minute)
	if cell, err := c.cellOf(context.Background(), tenant); err != nil || cell != "pe-02" {
		t.Fatalf("dentro del margen: %q %v", cell, err)
	}
	if got := counterValue("cell_resolution_stale_total", nil); got != antes+1 {
		t.Fatalf("cell_resolution_stale_total: %v, se esperaba %v", got, antes+1)
	}
	reloj.avanzar(cellStaleGrace)
	if cell, err := c.cellOf(context.Background(), tenant); !errors.Is(err, errCellUnresolved) {
		t.Fatalf("pasado el margen: %q %v", cell, err)
	}
	// Una baja definitiva sustituye a la ultima celda conocida.
	org.set(func(f *organizationFake) { f.down = false; delete(f.cells, tenant) })
	if _, err := c.cellOf(context.Background(), tenant); !errors.Is(err, errUnknownTenant) {
		t.Fatalf("empresa borrada: %v", err)
	}
}

func TestLaCacheEstaAcotada(t *testing.T) {
	a, b, d := uuid.NewString(), uuid.NewString(), uuid.NewString()
	org, srv := newOrganizationFake(t, map[string]string{a: "pe-01", b: "pe-02", d: "pe-03"})
	c, _ := nuevoResolver(srv.URL)
	c.max = 2
	for _, id := range []string{a, b, a, d} {
		if _, err := c.cellOf(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}
	if len(c.entries) != 2 || c.order.Len() != 2 {
		t.Fatalf("entradas: %d/%d", len(c.entries), c.order.Len())
	}
	// Se fue la usada hace mas tiempo (b); a, usada despues, sigue.
	antes := org.llamadas()
	if _, err := c.cellOf(context.Background(), a); err != nil || org.llamadas() != antes {
		t.Fatalf("a deberia seguir en la cache")
	}
	if _, err := c.cellOf(context.Background(), b); err != nil || org.llamadas() != antes+1 {
		t.Fatalf("b deberia haber salido de la cache")
	}
}

func TestPeticionesSimultaneasHacenUnaSolaConsulta(t *testing.T) {
	tenant := uuid.NewString()
	org, srv := newOrganizationFake(t, map[string]string{tenant: "pe-02"})
	release := make(chan struct{})
	org.set(func(f *organizationFake) { f.release = release })
	c, _ := nuevoResolver(srv.URL)

	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if cell, err := c.cellOf(context.Background(), tenant); err != nil || cell != "pe-02" {
				errs <- fmt.Errorf("%q %v", cell, err)
			}
		}()
	}
	// Un cliente que se va no deja sin respuesta a los demas.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.cellOf(ctx, tenant); !errors.Is(err, errCellUnresolved) {
		t.Errorf("cliente que corta: %v", err)
	}
	for org.llamadas() == 0 {
		time.Sleep(time.Millisecond)
	}
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if org.llamadas() != 1 {
		t.Fatalf("consultas a organization: %d", org.llamadas())
	}
}
