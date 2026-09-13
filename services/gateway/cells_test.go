package main

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/go-chi/chi/v5"
)

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
		{"ruta de antes sin celda a la celda por defecto", http.MethodGet, base + "release", "pe-01", http.StatusForbidden},
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

	// Una ruta publica que no existe sigue sin existir: no la atiende ninguna celda.
	hits = nil
	if got := call(gw, http.MethodGet, base+"pe-02/learn"+query); got.status != http.StatusNotFound && got.status != http.StatusMethodNotAllowed || len(hits) != 0 {
		t.Fatalf("accion inexistente: %d %v", got.status, hits)
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
	t.Setenv("MAIL_SECURITY_CELL_HOSTS", "pe-02")
	if _, err := loadRouteTable(); err == nil || !strings.Contains(err.Error(), "MAIL_SECURITY_CELL_HOSTS") {
		t.Fatalf("se esperaba error de MAIL_SECURITY_CELL_HOSTS: %v", err)
	}
}
