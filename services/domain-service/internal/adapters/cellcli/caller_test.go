package cellcli

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/tenantcell"
	"github.com/alonsosss/corforce-email/pkg/tenantcell/tenantcelltest"
	"github.com/google/uuid"
	dto "github.com/prometheus/client_model/go"
	"go.uber.org/zap"
)

const tokenDePrueba = "token-interno"

type llamada struct {
	instancia, metodo, ruta, empresa, token, cuerpo string
}

// instancias anota, sin carreras, que instancia recibio cada llamada y con que.
type instancias struct {
	mu       sync.Mutex
	llamadas []llamada
}

func (g *instancias) servidor(t *testing.T, nombre string, responder http.HandlerFunc) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cuerpo, _ := io.ReadAll(r.Body)
		g.mu.Lock()
		g.llamadas = append(g.llamadas, llamada{nombre, r.Method, r.URL.Path, r.Header.Get("X-Tenant-ID"), r.Header.Get("X-Gateway-Token"), string(cuerpo)})
		g.mu.Unlock()
		if responder != nil {
			responder(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func (g *instancias) tomar() []llamada {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := g.llamadas
	g.llamadas = nil
	return out
}

func responde(status int, cuerpo string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, cuerpo)
	}
}

func fallos(service, reason string) float64 {
	var m dto.Metric
	if err := failures.WithLabelValues(service, reason).Write(&m); err != nil {
		return -1
	}
	return m.GetCounter().GetValue()
}

type escenario struct {
	caller   *Caller
	g        *instancias
	org      *tenantcelltest.Organization
	empresas map[string]uuid.UUID
}

// dosCeldas: el destino base sirve pe-01 y hay una instancia en pe-02; pe-03 no tiene
// instancia. Cada prueba usa su propio nombre de servicio para leer sus contadores.
func dosCeldas(t *testing.T, service string, base, pe02 http.HandlerFunc) escenario {
	t.Helper()
	g := &instancias{}
	urlBase, urlPe02 := g.servidor(t, "pe-01", base), g.servidor(t, "pe-02", pe02)
	empresas := map[string]uuid.UUID{"base": uuid.New(), "otra": uuid.New(), "sin-instancia": uuid.New()}
	org, orgURL := tenantcelltest.New(t, tokenDePrueba, map[string]string{
		empresas["base"].String(): "pe-01", empresas["otra"].String(): "pe-02", empresas["sin-instancia"].String(): "pe-03",
	})
	inst := tenantcell.Instances{BaseCell: "pe-01", ByEnv: map[string]map[string]string{"X": {"pe-02": urlPe02}}}
	targets, err := inst.Targets("X", urlBase, tenantcell.NewResolver(orgURL, tokenDePrueba, zap.NewNop()))
	if err != nil {
		t.Fatal(err)
	}
	return escenario{caller: New(service, targets, tokenDePrueba, zap.NewNop()), g: g, org: org, empresas: empresas}
}

func (e escenario) do(t *testing.T, empresa uuid.UUID) (*http.Response, error) {
	t.Helper()
	resp, err := e.caller.Do(context.Background(), empresa, http.MethodPut, "/internal/svc/cosa", []byte(`{"a":1}`))
	if resp != nil {
		t.Cleanup(func() { resp.Body.Close() })
	}
	return resp, err
}

func TestLlevaCadaEmpresaALaInstanciaDeSuCelda(t *testing.T) {
	e := dosCeldas(t, "svc-rutas", nil, nil)
	for _, c := range []struct{ empresa, instancia string }{{"base", "pe-01"}, {"otra", "pe-02"}} {
		resp, err := e.do(t, e.empresas[c.empresa])
		if err != nil || resp.StatusCode != http.StatusNoContent {
			t.Fatalf("%s: %v", c.empresa, err)
		}
		got := e.g.tomar()
		want := llamada{c.instancia, http.MethodPut, "/internal/svc/cosa", e.empresas[c.empresa].String(), tokenDePrueba, `{"a":1}`}
		if len(got) != 1 || got[0] != want {
			t.Errorf("%s: %+v, se esperaba %+v", c.empresa, got, want)
		}
	}
}

// Sin instancia declarada, empresa desconocida u organization sin respuesta: la llamada no sale
// hacia ninguna instancia, el error lo dice y la metrica lo cuenta por motivo.
func TestSinCeldaOSinInstanciaNoSaleHaciaNinguna(t *testing.T) {
	e := dosCeldas(t, "svc-cerrado", nil, nil)

	resp, err := e.do(t, e.empresas["sin-instancia"])
	if resp != nil || !errors.Is(err, tenantcell.ErrNotServed) || !strings.Contains(err.Error(), "svc-cerrado") || !strings.Contains(err.Error(), "pe-03") {
		t.Errorf("celda sin instancia: %v", err)
	}
	if resp, err := e.do(t, uuid.New()); resp != nil || !errors.Is(err, tenantcell.ErrUnknownTenant) {
		t.Errorf("empresa desconocida: %v", err)
	}
	e.org.SetDown(true)
	if resp, err := e.do(t, e.empresas["base"]); resp != nil || !errors.Is(err, tenantcell.ErrUnresolved) {
		t.Errorf("organization caido y sin cache: %v", err)
	}
	if got := e.g.tomar(); len(got) != 0 {
		t.Errorf("salio hacia una instancia: %+v", got)
	}
	for reason, want := range map[string]float64{reasonNotServed: 1, reasonUnknown: 1, reasonUnresolved: 1, reasonNotInCell: 0} {
		if got := fallos("svc-cerrado", reason); got != want {
			t.Errorf("cell_call_failures_total{reason=%q} = %v, se esperaba %v", reason, got, want)
		}
	}
}

func TestConOrganizationCaidoValeLaCeldaYaConocida(t *testing.T) {
	e := dosCeldas(t, "svc-cache", nil, nil)
	if _, err := e.do(t, e.empresas["otra"]); err != nil {
		t.Fatal(err)
	}
	e.org.SetDown(true)
	if _, err := e.do(t, e.empresas["otra"]); err != nil {
		t.Fatalf("con la celda en cache: %v", err)
	}
	if got := e.g.tomar(); len(got) != 2 || got[0].instancia != "pe-02" || got[1].instancia != "pe-02" {
		t.Errorf("llamadas: %+v", got)
	}
}

// Una instancia que rechaza a la empresa por no ser de su celda es un error de configuracion:
// ni exito ni respuesta que interpretar, y se cuenta.
func TestTenantNotInCellEsErrorDeConfiguracion(t *testing.T) {
	e := dosCeldas(t, "svc-ajena", responde(http.StatusForbidden, `{"error":{"code":"TENANT_NOT_IN_CELL","message":"x"}}`), nil)
	resp, err := e.do(t, e.empresas["base"])
	if resp != nil || !errors.Is(err, ErrNotInCell) {
		t.Fatalf("403 TENANT_NOT_IN_CELL: %v %v", resp, err)
	}
	if got := len(e.g.tomar()); got != 1 {
		t.Errorf("intentos: %d; un 403 no se reintenta", got)
	}
	if got := fallos("svc-ajena", reasonNotInCell); got != 1 {
		t.Errorf("not_in_cell = %v", got)
	}
}

func TestLasDemasRespuestasLleganAQuienLlama(t *testing.T) {
	e := dosCeldas(t, "svc-otras", responde(http.StatusForbidden, `{"error":{"code":"FORBIDDEN","message":"no es tuyo"}}`), responde(http.StatusConflict, `{}`))
	resp, err := e.do(t, e.empresas["base"])
	if err != nil || resp.StatusCode != http.StatusForbidden || ErrorCode(resp) != "FORBIDDEN" {
		t.Fatalf("403 de otro codigo: %v %v", resp, err)
	}
	if body, _ := io.ReadAll(resp.Body); !strings.Contains(string(body), "no es tuyo") {
		t.Errorf("el cuerpo sigue legible: %q", body)
	}
	if resp, err := e.do(t, e.empresas["otra"]); err != nil || resp.StatusCode != http.StatusConflict {
		t.Errorf("409: %v %v", resp, err)
	}
	if got := fallos("svc-otras", reasonNotInCell); got != 0 {
		t.Errorf("not_in_cell = %v", got)
	}
}

func TestUnaCeldaVaAlDestinoBase(t *testing.T) {
	g := &instancias{}
	url := g.servidor(t, "unica", nil)
	targets, err := tenantcell.Instances{ByEnv: map[string]map[string]string{"X": {}}}.Targets("X", url, nil)
	if err != nil {
		t.Fatal(err)
	}
	caller := New("svc-una", targets, tokenDePrueba, nil)
	resp, err := caller.Do(context.Background(), uuid.New(), http.MethodDelete, "/internal/svc/cosa", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := g.tomar(); len(got) != 1 || got[0].instancia != "unica" || got[0].metodo != http.MethodDelete || got[0].cuerpo != "" {
		t.Errorf("llamadas: %+v", got)
	}
}

func TestErrorCodeSinSobre(t *testing.T) {
	resp := &http.Response{Body: io.NopCloser(strings.NewReader("404 page not found"))}
	if code := ErrorCode(resp); code != "" {
		t.Errorf("codigo %q", code)
	}
	if body, _ := io.ReadAll(resp.Body); string(body) != "404 page not found" {
		t.Errorf("cuerpo %q", body)
	}
}
