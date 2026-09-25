package main

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/tenantcell"
	"github.com/alonsosss/corforce-email/pkg/tenantcell/tenantcelltest"
	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

// llegada es lo que recibio una instancia del webmail.
type llegada struct {
	instancia, method, path, cookie, body string
	contentLength                         int64
}

type buzonDePrueba struct {
	mu       sync.Mutex
	llegadas []llegada
}

func (b *buzonDePrueba) take() []llegada {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := b.llegadas
	b.llegadas = nil
	return out
}

// webmailDePrueba hace de la instancia del webmail de una celda: anota lo que le llega, con el
// cuerpo entero, y responde con su propia CSP.
func webmailDePrueba(t *testing.T, b *buzonDePrueba, nombre string) (host, port string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		cookie := ""
		if c, err := r.Cookie("cf_wm"); err == nil {
			cookie = c.Value
		}
		b.mu.Lock()
		b.llegadas = append(b.llegadas, llegada{nombre, r.Method, r.URL.Path, cookie, string(body), r.ContentLength})
		b.mu.Unlock()
		w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{}}`)
	}))
	t.Cleanup(srv.Close)
	return hostPort(t, srv.URL)
}

// escenarioWebmail es el gateway de la tabla embebida con el webmail de pe-01 en el destino base
// y el de pe-02 en WEBMAIL_CELL_HOSTS (base vacia: despliegue de una celda, sin pe-02).
type escenarioWebmail struct {
	gw     http.Handler
	org    *tenantcelltest.Organization
	buzon  *buzonDePrueba
	strict int
}

func nuevoEscenarioWebmail(t *testing.T, base string) *escenarioWebmail {
	t.Helper()
	e := &escenarioWebmail{buzon: &buzonDePrueba{}}
	var orgURL string
	e.org, orgURL = tenantcelltest.New(t, "token-interno", nil)
	e.org.SetDomain("beta.test", "pe-02")
	e.org.SetDomain("acme.test", "pe-01")
	e.org.SetDomain("gamma.test", "pe-03")

	t.Setenv("GATEWAY_ROUTES_FILE", "")
	t.Setenv(baseCellEnv, base)
	t.Setenv("MAIL_DIRECTORY_CELL_HOSTS", "")
	t.Setenv("MAIL_SECURITY_CELL_HOSTS", "")
	host, port := hostPort(t, orgURL)
	t.Setenv("ORGANIZATION_HOST", host)
	t.Setenv("ORGANIZATION_HOST_PORT", port)
	host, port = webmailDePrueba(t, e.buzon, "pe-01")
	t.Setenv("WEBMAIL_HOST", host)
	t.Setenv("WEBMAIL_HOST_PORT", port)
	cells := ""
	if base != "" {
		host, port = webmailDePrueba(t, e.buzon, "pe-02")
		cells = "pe-02=" + net.JoinHostPort(host, port)
	}
	t.Setenv("WEBMAIL_CELL_HOSTS", cells)

	tbl, err := loadRouteTable()
	if err != nil {
		t.Fatal(err)
	}
	var domains *tenantcell.Resolver
	if tbl.baseCell != "" {
		domains = tenantcell.NewDomainResolver(tbl.serviceURL(cellDirectoryService), "token-interno", zap.NewNop())
	}
	strict := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			e.strict++
			next.ServeHTTP(w, r)
		})
	}
	r := chi.NewRouter()
	r.Use(middleware.StripInternalHeaders)
	r.Use(middleware.SecureHeaders)
	r.Route("/api/v1", func(r chi.Router) {
		mountSelfAuthenticated(r, tbl, strict, "token-interno", domains, zap.NewNop())
	})
	e.gw = r
	return e
}

func (e *escenarioWebmail) pedir(method, path, body, cookie string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	}
	req.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: "cf_wm", Value: cookie})
	}
	rec := httptest.NewRecorder()
	e.gw.ServeHTTP(rec, req)
	return rec
}

func (e *escenarioWebmail) login(body string) *httptest.ResponseRecorder {
	return e.pedir(http.MethodPost, "/api/v1/webmail/session", body, "")
}

const tokenPe02 = "pe-02.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

// El inicio de sesion va a la instancia de la celda del dominio del buzon, con el cuerpo intacto.
// Un dominio que organization no conoce va a la celda base, que lo rechaza como a una contrasena
// mala; una celda sin instancia recibe 503 sin salir hacia ninguna.
func TestElInicioDeSesionDelWebmailVaALaCeldaDelDominio(t *testing.T) {
	e := nuevoEscenarioWebmail(t, "pe-01")
	for _, c := range []struct{ caso, body, celda string }{
		{"dominio de pe-02", `{"username":"ana@beta.test","password":"secreta"}`, "pe-02"},
		{"con mayusculas y espacios, como lo normaliza el webmail", `{"username":"  Eva@BETA.test ","password":"secreta"}`, "pe-02"},
		{"dominio de la celda base", `{"username":"ana@acme.test","password":"secreta"}`, "pe-01"},
		{"dominio desconocido a la celda base", `{"username":"ana@nadie.test","password":"secreta"}`, "pe-01"},
		{"la clave con otras mayusculas, como la lee el webmail", `{"USERNAME":"ana@beta.test","password":"secreta"}`, "pe-02"},
		{"la ultima clave repetida, como la lee el webmail", `{"username":"ana@acme.test","username":"ana@beta.test","password":"x"}`, "pe-02"},
	} {
		rec := e.login(c.body)
		got := e.buzon.take()
		if rec.Code != http.StatusOK || len(got) != 1 || got[0].instancia != c.celda {
			t.Fatalf("%s: %d %+v", c.caso, rec.Code, got)
		}
		if got[0].body != c.body || got[0].contentLength != int64(len(c.body)) {
			t.Fatalf("%s: el cuerpo llega intacto: %q (%d)", c.caso, got[0].body, got[0].contentLength)
		}
		if csp := rec.Header().Values("Content-Security-Policy"); len(csp) != 2 {
			t.Fatalf("%s: se conserva la CSP del servicio: %q", c.caso, csp)
		}
	}
	if e.strict != 6 {
		t.Fatalf("cada inicio de sesion pasa por el limitador estricto: %d", e.strict)
	}

	// El inicio de sesion se enruta por el nombre de usuario, no por la cookie que ya traiga.
	rec := e.pedir(http.MethodPost, "/api/v1/webmail/session", `{"username":"ana@acme.test","password":"x"}`, tokenPe02)
	if got := e.buzon.take(); rec.Code != http.StatusOK || len(got) != 1 || got[0].instancia != "pe-01" {
		t.Fatalf("inicio con cookie de otra celda: %+v", got)
	}

	antes := counterValue("cell_routing_failures_total", map[string]string{"cell_service": "webmail", "reason": "not_served"})
	rec = e.login(`{"username":"ana@gamma.test","password":"secreta"}`)
	if rec.Code != http.StatusServiceUnavailable || codigoDeError(rec) != tenantcell.CodeCellUnavailable || len(e.buzon.take()) != 0 {
		t.Fatalf("celda sin instancia: %d %s", rec.Code, rec.Body)
	}
	if got := counterValue("cell_routing_failures_total", map[string]string{"cell_service": "webmail", "reason": "not_served"}); got != antes+1 {
		t.Fatalf("metrica not_served: %v", got)
	}
}

// Lo que no tiene un nombre de usuario con dominio va a la celda base con el cuerpo intacto y sin
// preguntar a organization: la celda base responde lo mismo que a una contrasena mala (o su 400).
// Un cuerpo mayor que el tope tampoco se interpreta, y el servicio lo recibe entero.
func TestElInicioDeSesionMalFormadoVaALaCeldaBase(t *testing.T) {
	e := nuevoEscenarioWebmail(t, "pe-01")
	grande := `{"username":"ana@beta.test","password":"` + strings.Repeat("a", maxCellLoginBody) + `"}`
	for caso, body := range map[string]string{
		"sin cuerpo":               "",
		"no es JSON":               `username=ana@beta.test`,
		"JSON sin nombre":          `{"password":"secreta"}`,
		"nombre que no es texto":   `{"username":12,"password":"secreta"}`,
		"nombre sin arroba":        `{"username":"ana","password":"secreta"}`,
		"arroba sin buzon":         `{"username":"@beta.test","password":"secreta"}`,
		"dominio mal formado":      `{"username":"ana@beta..test","password":"secreta"}`,
		"dominio con punto final":  `{"username":"ana@beta.test.","password":"secreta"}`,
		"dos arrobas":              `{"username":"ana@x@beta.test","password":"secreta"}`,
		"cuerpo mayor que el tope": grande,
	} {
		rec := e.login(body)
		got := e.buzon.take()
		if rec.Code != http.StatusOK || len(got) != 1 || got[0].instancia != "pe-01" || got[0].body != body {
			t.Fatalf("%s: %d %d llegadas", caso, rec.Code, len(got))
		}
	}
	if e.org.DomainCalls() != 0 {
		t.Fatalf("un cuerpo sin dominio pregunto a organization: %d", e.org.DomainCalls())
	}
}

// Con organization caido pasan los dominios ya resueltos; los demas reciben 503 sin salir hacia
// ninguna instancia, tambien uno que resultaria desconocido: sin respuesta no se sabe.
func TestElInicioDeSesionConOrganizationCaido(t *testing.T) {
	e := nuevoEscenarioWebmail(t, "pe-01")
	if rec := e.login(`{"username":"ana@beta.test","password":"x"}`); rec.Code != http.StatusOK {
		t.Fatalf("resolucion previa: %d", rec.Code)
	}
	e.buzon.take()
	e.org.SetDown(true)
	calls := e.org.DomainCalls()
	if rec := e.login(`{"username":"eva@beta.test","password":"x"}`); rec.Code != http.StatusOK || len(e.buzon.take()) != 1 || e.org.DomainCalls() != calls {
		t.Fatalf("dominio en cache con organization caido: %d", rec.Code)
	}

	antes := counterValue("cell_routing_failures_total", map[string]string{"cell_service": "webmail", "reason": "unresolved"})
	for _, body := range []string{`{"username":"ana@delta.test","password":"x"}`, `{"username":"ana@acme.test","password":"x"}`} {
		rec := e.login(body)
		if rec.Code != http.StatusServiceUnavailable || codigoDeError(rec) != tenantcell.CodeCellUnavailable || len(e.buzon.take()) != 0 {
			t.Fatalf("%s sin cache y con organization caido: %d %s", body, rec.Code, rec.Body)
		}
	}
	if got := counterValue("cell_routing_failures_total", map[string]string{"cell_service": "webmail", "reason": "unresolved"}); got != antes+2 {
		t.Fatalf("metrica unresolved: %v", got)
	}
}

// Despues del inicio, cada peticion va a la celda del prefijo del token de la cookie, sin
// preguntar a nadie. Un prefijo que no es una celda con instancia va a la celda base, donde la
// sesion no existe.
func TestLaSesionDelWebmailVaALaCeldaDeSuToken(t *testing.T) {
	e := nuevoEscenarioWebmail(t, "pe-01")
	for _, c := range []struct{ caso, cookie, celda string }{
		{"token de pe-02", tokenPe02, "pe-02"},
		{"token de la celda base", "pe-01.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "pe-01"},
		{"celda sin instancia", "zz-99.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "pe-01"},
		{"celda mal formada", "PE-02.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "pe-01"},
		{"token sin celda", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "pe-01"},
		{"sin cookie", "", "pe-01"},
	} {
		for _, rt := range []struct{ method, path string }{
			{http.MethodGet, "/api/v1/webmail/folders"},
			{http.MethodGet, "/api/v1/webmail/session"},
			{http.MethodDelete, "/api/v1/webmail/session"},
		} {
			rec := e.pedir(rt.method, rt.path, "", c.cookie)
			got := e.buzon.take()
			if rec.Code != http.StatusOK || len(got) != 1 || got[0].instancia != c.celda || got[0].cookie != c.cookie {
				t.Fatalf("%s, %s %s: %d %+v", c.caso, rt.method, rt.path, rec.Code, got)
			}
		}
	}
	if e.org.Calls() != 0 || e.strict != 0 {
		t.Fatalf("la sesion pregunto a organization (%d) o paso por el limitador estricto (%d)", e.org.Calls(), e.strict)
	}
}

// Sin celda base el despliegue es de una celda: todo va al destino base y no se pregunta a nadie.
func TestElWebmailDeUnaCeldaNoPreguntaANadie(t *testing.T) {
	e := nuevoEscenarioWebmail(t, "")
	if rec := e.login(`{"username":"ana@beta.test","password":"x"}`); rec.Code != http.StatusOK {
		t.Fatalf("inicio: %d", rec.Code)
	}
	if rec := e.pedir(http.MethodGet, "/api/v1/webmail/folders", "", tokenPe02); rec.Code != http.StatusOK {
		t.Fatalf("sesion: %d", rec.Code)
	}
	for _, got := range e.buzon.take() {
		if got.instancia != "pe-01" {
			t.Fatalf("una celda: %+v", got)
		}
	}
	if e.org.Calls() != 0 {
		t.Fatalf("una celda pregunto %d veces a organization", e.org.Calls())
	}
}

// El cuerpo que se lee para enrutar se devuelve entero al servicio aunque llegue troceado.
func TestElCuerpoDelInicioSeDevuelveEntero(t *testing.T) {
	e := nuevoEscenarioWebmail(t, "pe-01")
	body := `{"username":"ana@beta.test","password":"secreta"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webmail/session", io.MultiReader(bytes.NewReader([]byte(body[:10])), strings.NewReader(body[10:])))
	req.ContentLength = -1
	rec := httptest.NewRecorder()
	e.gw.ServeHTTP(rec, req)
	got := e.buzon.take()
	if rec.Code != http.StatusOK || len(got) != 1 || got[0].instancia != "pe-02" || got[0].body != body {
		t.Fatalf("cuerpo sin longitud: %d %+v", rec.Code, got)
	}
}

// El segundo paso del inicio de sesion aun no trae la cookie de sesion: va a la celda del token del
// desafio (cf_wm_mfa), con el limitador estricto. Con la cookie de sesion presente, manda esta.
func TestElSegundoPasoDelWebmailVaALaCeldaDelDesafio(t *testing.T) {
	e := nuevoEscenarioWebmail(t, "pe-01")
	pedir := func(cookies ...*http.Cookie) []llegada {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/webmail/session/mfa", strings.NewReader(`{"code":"123456"}`))
		req.Header.Set("Content-Type", "application/json")
		for _, c := range cookies {
			req.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		e.gw.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("segundo paso: %d", rec.Code)
		}
		return e.buzon.take()
	}
	if got := pedir(&http.Cookie{Name: "cf_wm_mfa", Value: tokenPe02}); len(got) != 1 || got[0].instancia != "pe-02" {
		t.Fatalf("el desafio de pe-02 va a pe-02: %+v", got)
	}
	if got := pedir(&http.Cookie{Name: "cf_wm_mfa", Value: "sin-celda"}); len(got) != 1 || got[0].instancia != "pe-01" {
		t.Fatalf("un desafio sin celda va a la base: %+v", got)
	}
	if got := pedir(&http.Cookie{Name: "cf_wm", Value: "pe-01.x"}, &http.Cookie{Name: "cf_wm_mfa", Value: tokenPe02}); len(got) != 1 || got[0].instancia != "pe-01" {
		t.Fatalf("la cookie de sesion manda sobre la del desafio: %+v", got)
	}
	if e.strict != 3 {
		t.Fatalf("el segundo paso pasa por el limitador estricto: %d", e.strict)
	}
}
