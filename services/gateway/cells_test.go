package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/tenantcell"
	"github.com/alonsosss/corforce-email/pkg/tenantcell/tenantcelltest"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

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

// fakeCell hace de mail-security de una celda: acepta el enlace de su celda con la firma
// buena y responde a todo lo demas con la misma pagina, como el servicio real.
func fakeCell(t *testing.T, cell string, hits *[]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*hits = append(*hits, cell+" "+r.Method+" "+r.URL.RequestURI()+" token="+r.Header.Get("X-Gateway-Token")+" tenant="+r.Header.Get("X-Tenant-ID"))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		segs := strings.Split(r.URL.Path, "/")
		if len(segs) > 2 && segs[len(segs)-2] == cell && r.URL.Query().Get("sig") == "buena" {
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "liberado en "+cell)
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "<h1>Enlace no valido</h1>")
	}))
	t.Cleanup(srv.Close)
	return srv
}

func hostPort(t *testing.T, raw string) (string, string) {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatal(err)
	}
	return host, port
}

// newCellGateway monta las rutas publicas de la tabla embebida con la celda por defecto
// (pe-01) en el destino base y pe-02 declarada en MAIL_SECURITY_CELL_HOSTS.
func newCellGateway(t *testing.T, hits *[]string) http.Handler {
	t.Helper()
	pe01, pe02 := fakeCell(t, "pe-01", hits), fakeCell(t, "pe-02", hits)
	host, port := hostPort(t, pe01.URL)
	t.Setenv("MAIL_SECURITY_HOST", host)
	t.Setenv("MAIL_SECURITY_HOST_PORT", port)
	host, port = hostPort(t, pe02.URL)
	t.Setenv("MAIL_SECURITY_CELL_HOSTS", "pe-02="+net.JoinHostPort(host, port))
	t.Setenv("MAIL_DIRECTORY_CELL_HOSTS", "")
	t.Setenv(baseCellEnv, "pe-01")
	t.Setenv("GATEWAY_ROUTES_FILE", "")
	tbl, err := loadRouteTable()
	if err != nil {
		t.Fatal(err)
	}
	if codes := tbl.cellCodes()["mail-security"]; len(codes) != 1 || codes[0] != "pe-02" {
		t.Fatalf("instancias por celda: %v", codes)
	}
	r := chi.NewRouter()
	r.Use(middleware.StripInternalHeaders)
	r.Route("/api/v1", func(r chi.Router) { mountPublic(r, tbl, "token-interno") })
	return r
}

type gatewayResponse struct {
	status               int
	body, ctype, caching string
}

func call(h http.Handler, method, target string) gatewayResponse {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, target, nil)
	// Una cabecera de identidad del cliente no llega al servicio.
	req.Header.Set("X-Tenant-ID", "00000000-0000-0000-0000-000000000001")
	h.ServeHTTP(rec, req)
	return gatewayResponse{rec.Code, rec.Body.String(), rec.Header().Get("Content-Type"), rec.Header().Get("Cache-Control")}
}

func TestEnlacesDeCuarentenaSeEnrutanPorCelda(t *testing.T) {
	var hits []string
	gw := newCellGateway(t, &hits)
	const query = "?t=x&q=y&e=1&sig=buena"
	base := "/api/v1/public/mail-security/quarantine/"

	for _, tc := range []struct {
		name, method, path, cell string
		status                   int
	}{
		{"celda declarada", http.MethodPost, base + "pe-02/release", "pe-02", http.StatusOK},
		{"celda declarada, GET", http.MethodGet, base + "pe-02/discard", "pe-02", http.StatusOK},
		{"celda por defecto sin instancia propia", http.MethodPost, base + "pe-01/discard", "pe-01", http.StatusOK},
		{"celda desconocida a la celda por defecto", http.MethodPost, base + "zz-99/release", "pe-01", http.StatusForbidden},
		{"segmento con otra forma a la celda por defecto", http.MethodGet, base + "PE-02/release", "pe-01", http.StatusForbidden},
	} {
		hits = nil
		got := call(gw, tc.method, tc.path+query)
		if got.status != tc.status || len(hits) != 1 {
			t.Fatalf("%s: status %d, llamadas %v", tc.name, got.status, hits)
		}
		want := tc.cell + " " + tc.method + " " + tc.path + query + " token=token-interno tenant="
		if hits[0] != want {
			t.Fatalf("%s: llego %q, se esperaba %q", tc.name, hits[0], want)
		}
	}

	// Una ruta publica que no existe sigue sin existir: no la atiende ninguna celda. Tampoco
	// la ruta sin celda.
	for _, path := range []string{base + "pe-02/learn", base + "release", base + "discard"} {
		hits = nil
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			if got := call(gw, method, path+query); got.status != http.StatusNotFound && got.status != http.StatusMethodNotAllowed || len(hits) != 0 {
				t.Fatalf("%s %s: %d %v", method, path, got.status, hits)
			}
		}
	}
}

// Una celda desconocida no se distingue de una firma alterada en una celda real: la misma
// respuesta, porque la da el mismo servicio y el gateway no pone nada propio.
func TestCeldaDesconocidaIgualQueFirmaAlterada(t *testing.T) {
	var hits []string
	gw := newCellGateway(t, &hits)
	base := "/api/v1/public/mail-security/quarantine/"
	badSignature := call(gw, http.MethodPost, base+"pe-02/release?t=x&q=y&e=1&sig=alterada")
	unknownCell := call(gw, http.MethodPost, base+"zz-99/release?t=x&q=y&e=1&sig=buena")
	tampered := call(gw, http.MethodPost, base+"pe-01/release?t=x&q=y&e=1&sig=alterada")
	if badSignature.status != http.StatusForbidden || unknownCell != badSignature || tampered != badSignature {
		t.Fatalf("respuestas distintas:\nfirma alterada %+v\ncelda desconocida %+v\nsegmento cambiado %+v", badSignature, unknownCell, tampered)
	}
}

func TestInstanciasPorCeldaDelEntorno(t *testing.T) {
	got, err := parseCellHosts("X", " pe-01=mail-security-pe-01:8042 , pe-02=10.0.2.15:9042,eu-west-1=[fd00::5]:8042")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"pe-01": "http://mail-security-pe-01:8042", "pe-02": "http://10.0.2.15:9042", "eu-west-1": "http://[fd00::5]:8042"}
	if len(got) != len(want) {
		t.Fatalf("instancias: %v", got)
	}
	for code, target := range want {
		if got[code] != target {
			t.Errorf("%s: %q, se esperaba %q", code, got[code], target)
		}
	}
	if got, err := parseCellHosts("X", "  "); err != nil || len(got) != 0 {
		t.Fatalf("vacia: %v %v", got, err)
	}

	for name, raw := range map[string]string{
		"sin igual":             "pe-01",
		"sin puerto":            "pe-01=mail-security",
		"puerto no numerico":    "pe-01=mail-security:http",
		"puerto fuera de rango": "pe-01=mail-security:70000",
		"puerto cero":           "pe-01=mail-security:0",
		"con esquema":           "pe-01=http://mail-security:8042",
		"con ruta":              "pe-01=mail-security/x:8042",
		"sin host":              "pe-01=:8042",
		"celda repetida":        "pe-01=a:1,pe-01=b:2",
		"celda en mayusculas":   "PE-01=a:1",
		"celda con guion final": "pe-=a:1",
		"celda vacia":           "=a:1",
		"entrada vacia":         "pe-01=a:1,,pe-02=b:2",
	} {
		if _, err := parseCellHosts("X", raw); err == nil {
			t.Errorf("%s: %q se esperaba error", name, raw)
		}
	}
}

// Una variable de instancias mal formada impide arrancar.
func TestInstanciasMalFormadasNoArrancan(t *testing.T) {
	t.Setenv("GATEWAY_ROUTES_FILE", "")
	t.Setenv(baseCellEnv, "pe-01")
	t.Setenv("MAIL_DIRECTORY_CELL_HOSTS", "")
	t.Setenv("MAIL_SECURITY_CELL_HOSTS", "pe-02")
	if _, err := loadRouteTable(); err == nil || !strings.Contains(err.Error(), "MAIL_SECURITY_CELL_HOSTS") {
		t.Fatalf("se esperaba error de MAIL_SECURITY_CELL_HOSTS: %v", err)
	}
}

// La celda de los destinos base es obligatoria en cuanto se declara una instancia por celda,
// bien formada y no repetida como instancia; sin nada declarado el despliegue es de una celda.
func TestCeldaBaseDelEntorno(t *testing.T) {
	t.Setenv("GATEWAY_ROUTES_FILE", "")
	for nombre, c := range map[string]struct{ base, directory, security, err string }{
		"instancias sin celda base":           {"", "", "pe-02=ms-pe-02:8042", baseCellEnv},
		"celda base mal formada":              {"PE-01", "", "", baseCellEnv},
		"celda base tambien como instancia":   {"pe-01", "pe-01=md-pe-01:8040", "", "MAIL_DIRECTORY_CELL_HOSTS"},
		"celda base con instancias de otra":   {"pe-01", "pe-02=md-pe-02:8040", "pe-02=ms-pe-02:8042", ""},
		"solo celda base, sin otras celdas":   {"pe-01", "", "", ""},
		"una celda: nada declarado":           {"", "", "", ""},
		"celda base con espacios alrededor":   {" pe-01 ", "", "", ""},
		"instancias sin celda base (directo)": {"", "pe-02=md-pe-02:8040", "", "MAIL_DIRECTORY_CELL_HOSTS"},
	} {
		t.Setenv(baseCellEnv, c.base)
		t.Setenv("MAIL_DIRECTORY_CELL_HOSTS", c.directory)
		t.Setenv("MAIL_SECURITY_CELL_HOSTS", c.security)
		tbl, err := loadRouteTable()
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s: se esperaba error con %s: %v", nombre, c.err, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", nombre, err)
			continue
		}
		if want := strings.TrimSpace(c.base); tbl.baseCell != want {
			t.Errorf("%s: celda base %q, se esperaba %q", nombre, tbl.baseCell, want)
		}
	}
}

// Una celda declarada para un servicio de celda y no para otro se avisa al arrancar: sus
// empresas tendran 503 en el que falta.
func TestCeldasSinInstanciaDeUnServicio(t *testing.T) {
	t.Setenv("GATEWAY_ROUTES_FILE", "")
	t.Setenv(baseCellEnv, "pe-01")
	t.Setenv("MAIL_DIRECTORY_CELL_HOSTS", "pe-02=md-pe-02:8040")
	t.Setenv("MAIL_SECURITY_CELL_HOSTS", "pe-02=ms-pe-02:8042,pe-03=ms-pe-03:8042")
	tbl, err := loadRouteTable()
	if err != nil {
		t.Fatal(err)
	}
	gaps := tbl.cellCoverageGaps()
	if len(gaps) != 1 || strings.Join(gaps["mail-directory"], ",") != "pe-03" {
		t.Fatalf("huecos: %v", gaps)
	}
}

// grabador guarda, sin carreras, que instancia recibio cada peticion.
type grabador struct {
	mu   sync.Mutex
	hits []string
}

func (g *grabador) add(s string) {
	g.mu.Lock()
	g.hits = append(g.hits, s)
	g.mu.Unlock()
}

func (g *grabador) take() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := g.hits
	g.hits = nil
	return out
}

// instancia hace de un servicio en una celda: anota lo que recibe con las cabeceras que el
// gateway le pone.
func instancia(t *testing.T, g *grabador, nombre string) (host, port string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.add(nombre + " " + r.Method + " " + r.URL.Path + " tenant=" + r.Header.Get("X-Tenant-ID") + " token=" + r.Header.Get("X-Gateway-Token"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	t.Cleanup(srv.Close)
	return hostPort(t, srv.URL)
}

// cabeceraDePrueba lleva la empresa al contexto como lo haria el JWT verificado. El gateway
// borra X-Tenant-ID del cliente; esta cabecera solo existe en la prueba.
const cabeceraDePrueba = "X-Prueba-Tenant"

// newSessionGateway monta las rutas con sesion de la tabla embebida como main, con cada
// servicio de celda en pe-01 (destino base) y pe-02, templates como servicio que no es de
// celda y organization en org. base vacia es un despliegue de una celda.
func newSessionGateway(t *testing.T, g *grabador, orgURL, base string) http.Handler {
	t.Helper()
	t.Setenv("GATEWAY_ROUTES_FILE", "")
	t.Setenv(baseCellEnv, base)
	host, port := hostPort(t, orgURL)
	t.Setenv("ORGANIZATION_HOST", host)
	t.Setenv("ORGANIZATION_HOST_PORT", port)
	host, port = instancia(t, g, "templates")
	t.Setenv("TEMPLATES_HOST", host)
	t.Setenv("TEMPLATES_HOST_PORT", port)
	for _, s := range []struct{ name, env string }{{"mail-directory", "MAIL_DIRECTORY"}, {"mail-security", "MAIL_SECURITY"}} {
		host, port = instancia(t, g, s.name+" pe-01")
		t.Setenv(s.env+"_HOST", host)
		t.Setenv(s.env+"_HOST_PORT", port)
		cells := ""
		if base != "" {
			host, port = instancia(t, g, s.name+" pe-02")
			cells = "pe-02=" + net.JoinHostPort(host, port)
		}
		t.Setenv(s.env+"_CELL_HOSTS", cells)
	}
	tbl, err := loadRouteTable()
	if err != nil {
		t.Fatal(err)
	}
	var cells *tenantcell.Resolver
	if tbl.baseCell != "" {
		cells = tenantcell.NewResolver(tbl.serviceURL(cellDirectoryService), "token-interno", zap.NewNop())
	}
	handlers := sessionHandlers(tbl, "token-interno", cells, zap.NewNop())
	r := chi.NewRouter()
	r.Use(middleware.StripInternalHeaders)
	r.Route("/api/v1", func(r chi.Router) {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				next.ServeHTTP(w, r.WithContext(middleware.WithTenantID(r.Context(), r.Header.Get(cabeceraDePrueba))))
			})
		})
		for _, rt := range tbl.Routes {
			proxy := handlers[rt.Service]
			r.Route("/"+rt.Prefix, func(r chi.Router) { r.Handle("/*", proxy) })
		}
	})
	return r
}

func pedirComo(h http.Handler, tenant, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set(cabeceraDePrueba, tenant)
	req.Header.Set("X-Tenant-ID", uuid.NewString())
	h.ServeHTTP(rec, req)
	return rec
}

func codigoDeError(rec *httptest.ResponseRecorder) string {
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return body.Error.Code
}

// Cada ruta con sesion de cada servicio de celda va a la instancia de la celda de su empresa;
// una celda sin instancia, una empresa desconocida u organization sin respuesta no salen hacia
// ninguna. Los servicios que no son de celda no preguntan la celda.
func TestLasRutasConSesionDeCadaServicioDeCeldaVanASuCelda(t *testing.T) {
	t01, t02, t03, nadie := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	org, orgURL := tenantcelltest.New(t, "token-interno", map[string]string{t01: "pe-01", t02: "pe-02", t03: "pe-03"})
	g := &grabador{}
	gw := newSessionGateway(t, g, orgURL, "pe-01")
	tbl, err := loadRouteTable()
	if err != nil {
		t.Fatal(err)
	}

	cubiertos := map[string]bool{}
	for _, rt := range tbl.Routes {
		if tbl.Services[rt.Service].CellHostsEnv == "" {
			continue
		}
		cubiertos[rt.Service] = true
		path := "/api/v1/" + rt.Prefix + "/x"
		for tenant, cell := range map[string]string{t01: "pe-01", t02: "pe-02"} {
			rec := pedirComo(gw, tenant, path)
			hits := g.take()
			want := rt.Service + " " + cell + " GET " + path + " tenant=" + tenant + " token=token-interno"
			if rec.Code != http.StatusOK || len(hits) != 1 || hits[0] != want {
				t.Fatalf("%s en %s: %d %v, se esperaba %q", path, cell, rec.Code, hits, want)
			}
		}

		antes := counterValue("cell_routing_failures_total", map[string]string{"service": rt.Service, "reason": "not_served"})
		rec := pedirComo(gw, t03, path)
		if rec.Code != http.StatusServiceUnavailable || codigoDeError(rec) != "CELL_UNAVAILABLE" || len(g.take()) != 0 {
			t.Fatalf("%s en una celda sin instancia: %d %s", path, rec.Code, rec.Body)
		}
		if got := counterValue("cell_routing_failures_total", map[string]string{"service": rt.Service, "reason": "not_served"}); got != antes+1 {
			t.Fatalf("metrica not_served de %s: %v", rt.Service, got)
		}
		if rec := pedirComo(gw, nadie, path); rec.Code != http.StatusForbidden || len(g.take()) != 0 {
			t.Fatalf("%s con una empresa desconocida: %d %s", path, rec.Code, rec.Body)
		}
		if rec := pedirComo(gw, "", path); rec.Code != http.StatusForbidden || len(g.take()) != 0 {
			t.Fatalf("%s sin empresa: %d", path, rec.Code)
		}
	}
	if !cubiertos["mail-directory"] || !cubiertos["mail-security"] {
		t.Fatalf("servicios de celda recorridos: %v", cubiertos)
	}

	antes := org.Calls()
	if rec := pedirComo(gw, t02, "/api/v1/templates/x"); rec.Code != http.StatusOK || strings.Join(g.take(), "") != "templates GET /api/v1/templates/x tenant="+t02+" token=token-interno" {
		t.Fatalf("un servicio que no es de celda va a su destino: %d", rec.Code)
	}
	if org.Calls() != antes {
		t.Fatalf("un servicio que no es de celda pregunto la celda")
	}

	// organization caido y sin respuesta previa: 503, a ninguna instancia.
	sinRespuesta := uuid.NewString()
	org.SetDown(true)
	org.SetCell(sinRespuesta, "pe-01")
	antes2 := counterValue("cell_routing_failures_total", map[string]string{"service": "mail-directory", "reason": "unresolved"})
	rec := pedirComo(gw, sinRespuesta, "/api/v1/mailboxes/x")
	if rec.Code != http.StatusServiceUnavailable || codigoDeError(rec) != "CELL_UNAVAILABLE" || len(g.take()) != 0 {
		t.Fatalf("organization caido: %d %s", rec.Code, rec.Body)
	}
	if got := counterValue("cell_routing_failures_total", map[string]string{"service": "mail-directory", "reason": "unresolved"}); got != antes2+1 {
		t.Fatalf("metrica unresolved: %v", got)
	}
	// Con la respuesta en cache siguen pasando las empresas ya resueltas.
	if rec := pedirComo(gw, t02, "/api/v1/mailboxes/x"); rec.Code != http.StatusOK || len(g.take()) != 1 {
		t.Fatalf("empresa resuelta con organization caido: %d", rec.Code)
	}
}

// Sin celda base ni instancias (una celda), todo va al destino base y organization no se
// consulta: el despliegue de una celda no necesita configuracion nueva.
func TestUnaSolaCeldaNoPreguntaLaCelda(t *testing.T) {
	org, orgURL := tenantcelltest.New(t, "token-interno", map[string]string{})
	g := &grabador{}
	gw := newSessionGateway(t, g, orgURL, "")
	tenant := uuid.NewString()
	for _, path := range []string{"/api/v1/mailboxes/x", "/api/v1/mail-domains", "/api/v1/mail-security/quarantine", "/api/v1/templates/x"} {
		rec := pedirComo(gw, tenant, path)
		hits := g.take()
		if rec.Code != http.StatusOK || len(hits) != 1 || strings.Contains(hits[0], "pe-02") {
			t.Fatalf("%s: %d %v", path, rec.Code, hits)
		}
	}
	if org.Calls() != 0 {
		t.Fatalf("una celda pregunto %d veces a organization", org.Calls())
	}
}
