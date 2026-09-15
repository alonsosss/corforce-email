package tenantcell

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/tenantcell/tenantcelltest"
	"github.com/google/uuid"
	dto "github.com/prometheus/client_model/go"
	"go.uber.org/zap"
)

type llamadaRecibida struct {
	instancia, metodo, ruta, empresa, token, cuerpo string
}

// instanciasDePrueba anota, sin carreras, que instancia recibio cada llamada y con que.
type instanciasDePrueba struct {
	mu       sync.Mutex
	llamadas []llamadaRecibida
}

func (g *instanciasDePrueba) servidor(t *testing.T, nombre string, responder http.HandlerFunc) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cuerpo, _ := io.ReadAll(r.Body)
		g.mu.Lock()
		g.llamadas = append(g.llamadas, llamadaRecibida{nombre, r.Method, r.URL.Path, r.Header.Get("X-Tenant-ID"), r.Header.Get("X-Gateway-Token"), string(cuerpo)})
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

func (g *instanciasDePrueba) tomar() []llamadaRecibida {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := g.llamadas
	g.llamadas = nil
	return out
}

func respondeCon(status int, cuerpo string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, cuerpo)
	}
}

func fallosDeLlamada(service, reason string) float64 {
	var m dto.Metric
	if err := callFailures.WithLabelValues(service, reason).Write(&m); err != nil {
		return -1
	}
	return m.GetCounter().GetValue()
}

type escenarioDeLlamadas struct {
	caller   *Caller
	g        *instanciasDePrueba
	org      *tenantcelltest.Organization
	empresas map[string]uuid.UUID
}

// dosCeldasDeLlamadas: el destino base sirve pe-01 y hay una instancia en pe-02; pe-03 no tiene
// instancia. Cada prueba usa su propio nombre de servicio para leer sus contadores.
func dosCeldasDeLlamadas(t *testing.T, service string, base, pe02 http.HandlerFunc) escenarioDeLlamadas {
	t.Helper()
	g := &instanciasDePrueba{}
	urlBase, urlPe02 := g.servidor(t, "pe-01", base), g.servidor(t, "pe-02", pe02)
	empresas := map[string]uuid.UUID{"base": uuid.New(), "otra": uuid.New(), "sin-instancia": uuid.New()}
	org, orgURL := tenantcelltest.New(t, tokenDePrueba, map[string]string{
		empresas["base"].String(): "pe-01", empresas["otra"].String(): "pe-02", empresas["sin-instancia"].String(): "pe-03",
	})
	inst := Instances{BaseCell: "pe-01", ByEnv: map[string]map[string]string{"X": {"pe-02": urlPe02}}}
	targets, err := inst.Targets("X", urlBase, NewResolver(orgURL, tokenDePrueba, zap.NewNop()))
	if err != nil {
		t.Fatal(err)
	}
	return escenarioDeLlamadas{caller: NewCaller(service, targets, tokenDePrueba, zap.NewNop(), CallerOptions{}), g: g, org: org, empresas: empresas}
}

func (e escenarioDeLlamadas) do(t *testing.T, empresa uuid.UUID) (*http.Response, error) {
	t.Helper()
	resp, err := e.caller.Do(context.Background(), empresa, http.MethodPut, "/internal/svc/cosa", []byte(`{"a":1}`))
	if resp != nil {
		t.Cleanup(func() { resp.Body.Close() })
	}
	return resp, err
}

func TestLlevaCadaEmpresaALaInstanciaDeSuCelda(t *testing.T) {
	e := dosCeldasDeLlamadas(t, "svc-rutas", nil, nil)
	for _, c := range []struct{ empresa, instancia string }{{"base", "pe-01"}, {"otra", "pe-02"}} {
		resp, err := e.do(t, e.empresas[c.empresa])
		if err != nil || resp.StatusCode != http.StatusNoContent {
			t.Fatalf("%s: %v", c.empresa, err)
		}
		got := e.g.tomar()
		want := llamadaRecibida{c.instancia, http.MethodPut, "/internal/svc/cosa", e.empresas[c.empresa].String(), tokenDePrueba, `{"a":1}`}
		if len(got) != 1 || got[0] != want {
			t.Errorf("%s: %+v, se esperaba %+v", c.empresa, got, want)
		}
	}
}

// Sin instancia declarada, empresa desconocida u organization sin respuesta: la llamada no sale
// hacia ninguna instancia, el error lo dice y la metrica lo cuenta por motivo.
func TestSinCeldaOSinInstanciaNoSaleHaciaNinguna(t *testing.T) {
	e := dosCeldasDeLlamadas(t, "svc-cerrado", nil, nil)

	resp, err := e.do(t, e.empresas["sin-instancia"])
	if resp != nil || !errors.Is(err, ErrNotServed) || !strings.Contains(err.Error(), "svc-cerrado") || !strings.Contains(err.Error(), "pe-03") {
		t.Errorf("celda sin instancia: %v", err)
	}
	if resp, err := e.do(t, uuid.New()); resp != nil || !errors.Is(err, ErrUnknownTenant) {
		t.Errorf("empresa desconocida: %v", err)
	}
	e.org.SetDown(true)
	if resp, err := e.do(t, e.empresas["base"]); resp != nil || !errors.Is(err, ErrUnresolved) {
		t.Errorf("organization caido y sin cache: %v", err)
	}
	if got := e.g.tomar(); len(got) != 0 {
		t.Errorf("salio hacia una instancia: %+v", got)
	}
	for reason, want := range map[string]float64{callReasonNotServed: 1, callReasonUnknown: 1, callReasonUnresolved: 1, callReasonNotInCell: 0} {
		if got := fallosDeLlamada("svc-cerrado", reason); got != want {
			t.Errorf("cell_call_failures_total{reason=%q} = %v, se esperaba %v", reason, got, want)
		}
	}
}

func TestConOrganizationCaidoValeLaCeldaYaConocida(t *testing.T) {
	e := dosCeldasDeLlamadas(t, "svc-cache", nil, nil)
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
	e := dosCeldasDeLlamadas(t, "svc-ajena", respondeCon(http.StatusForbidden, `{"error":{"code":"TENANT_NOT_IN_CELL","message":"x"}}`), nil)
	resp, err := e.do(t, e.empresas["base"])
	if resp != nil || !errors.Is(err, ErrNotInCell) {
		t.Fatalf("403 TENANT_NOT_IN_CELL: %v %v", resp, err)
	}
	if got := len(e.g.tomar()); got != 1 {
		t.Errorf("intentos: %d; un 403 no se reintenta", got)
	}
	if got := fallosDeLlamada("svc-ajena", callReasonNotInCell); got != 1 {
		t.Errorf("not_in_cell = %v", got)
	}
}

func TestLasDemasRespuestasLleganAQuienLlama(t *testing.T) {
	e := dosCeldasDeLlamadas(t, "svc-otras", respondeCon(http.StatusForbidden, `{"error":{"code":"FORBIDDEN","message":"no es tuyo"}}`), respondeCon(http.StatusConflict, `{}`))
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
	if got := fallosDeLlamada("svc-otras", callReasonNotInCell); got != 0 {
		t.Errorf("not_in_cell = %v", got)
	}
}

func TestUnaLlamadaEnUnaCeldaVaAlDestinoBase(t *testing.T) {
	g := &instanciasDePrueba{}
	url := g.servidor(t, "unica", nil)
	targets, err := Instances{ByEnv: map[string]map[string]string{"X": {}}}.Targets("X", url, nil)
	if err != nil {
		t.Fatal(err)
	}
	caller := NewCaller("svc-una", targets, tokenDePrueba, nil, CallerOptions{})
	resp, err := caller.Do(context.Background(), uuid.New(), http.MethodDelete, "/internal/svc/cosa", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := g.tomar(); len(got) != 1 || got[0].instancia != "unica" || got[0].metodo != http.MethodDelete || got[0].cuerpo != "" {
		t.Errorf("llamadas: %+v", got)
	}
}

// Quien ya sabe la celda de la empresa (organization) elige la instancia por ella, sin resolver
// nada: la base para su celda, la instancia declarada para otra y ninguna para una celda sin
// instancia, que no sale hacia ninguna y se cuenta. Las garantias de la instancia son las mismas.
func TestDoInCellEligePorLaCeldaSinPreguntar(t *testing.T) {
	g := &instanciasDePrueba{}
	urlBase := g.servidor(t, "pe-01", nil)
	urlPe02 := g.servidor(t, "pe-02", respondeCon(http.StatusForbidden, `{"error":{"code":"TENANT_NOT_IN_CELL","message":"x"}}`))
	inst := Instances{BaseCell: "pe-01", ByEnv: map[string]map[string]string{"X": {"pe-02": urlPe02}}}
	targets, err := inst.CellTargets("X", urlBase)
	if err != nil {
		t.Fatal(err)
	}
	caller := NewCaller("svc-celda", targets, tokenDePrueba, zap.NewNop(), CallerOptions{})
	empresa := uuid.New()
	ctx := context.Background()

	resp, err := caller.DoInCell(ctx, "pe-01", empresa, http.MethodPut, "/internal/svc/baja", nil)
	if err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("celda base: %v", err)
	}
	resp.Body.Close()
	want := llamadaRecibida{"pe-01", http.MethodPut, "/internal/svc/baja", empresa.String(), tokenDePrueba, ""}
	if got := g.tomar(); len(got) != 1 || got[0] != want {
		t.Fatalf("celda base: %+v, se esperaba %+v", got, want)
	}

	if resp, err := caller.DoInCell(ctx, "pe-02", empresa, http.MethodPut, "/internal/svc/baja", nil); resp != nil || !errors.Is(err, ErrNotInCell) {
		t.Fatalf("instancia que rechaza a la empresa: %v %v", resp, err)
	}
	if got := g.tomar(); len(got) != 1 || got[0].instancia != "pe-02" {
		t.Fatalf("otra celda: %+v", got)
	}

	if resp, err := caller.DoInCell(ctx, "pe-03", empresa, http.MethodPut, "/internal/svc/baja", nil); resp != nil || !errors.Is(err, ErrNotServed) {
		t.Fatalf("celda sin instancia: %v %v", resp, err)
	}
	if got := g.tomar(); len(got) != 0 {
		t.Fatalf("salio hacia una instancia: %+v", got)
	}
	for reason, want := range map[string]float64{callReasonNotServed: 1, callReasonNotInCell: 1, callReasonUnresolved: 0, callReasonUnknown: 0} {
		if got := fallosDeLlamada("svc-celda", reason); got != want {
			t.Errorf("cell_call_failures_total{reason=%q} = %v, se esperaba %v", reason, got, want)
		}
	}
}

// El selector sin resolver solo elige por celda: For no adivina, y en un despliegue de una celda
// cualquier celda va al destino base, como hace For.
func TestCellTargetsNoResuelve(t *testing.T) {
	inst := Instances{BaseCell: "pe-01", ByEnv: map[string]map[string]string{"X": {"pe-02": "http://pe02:1"}}}
	targets, err := inst.CellTargets("X", "http://base:1/")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := targets.For(context.Background(), uuid.NewString()); !errors.Is(err, ErrUnresolved) {
		t.Fatalf("For sin resolver: %v", err)
	}
	for cell, want := range map[string]string{"pe-01": "http://base:1", "pe-02": "http://pe02:1"} {
		if got, err := targets.ForCell(cell); err != nil || got != want {
			t.Errorf("ForCell(%s) = %q, %v; se esperaba %q", cell, got, err, want)
		}
	}
	if _, err := inst.Targets("X", "http://base:1", nil); err == nil {
		t.Error("Targets con varias celdas y sin resolver debe fallar al configurar")
	}
	una, err := Instances{ByEnv: map[string]map[string]string{"X": {}}}.CellTargets("X", "http://base:1")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := una.ForCell("pe-07"); err != nil || got != "http://base:1" {
		t.Errorf("una celda: %q, %v", got, err)
	}
	if _, err := (Instances{ByEnv: map[string]map[string]string{}}).CellTargets("X", "http://base:1"); err == nil {
		t.Error("sin las instancias de la variable cargadas debe fallar")
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
