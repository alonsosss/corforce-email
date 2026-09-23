package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/tenantcell"
	"github.com/alonsosss/corforce-email/pkg/tenantcell/tenantcelltest"
	handler "github.com/alonsosss/corforce-email/services/mail-security/internal/adapters/http"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const tokenInterno = "token-interno"

var paramRe = regexp.MustCompile(`\{[^}]+\}`)

// celdaPe01 monta el router de main como la instancia de la celda pe-01, con organization de
// prueba.
func celdaPe01(t *testing.T, cells map[string]string, routes http.Handler) (*tenantcelltest.Organization, http.Handler) {
	t.Helper()
	t.Setenv("INTERNAL_GATEWAY_TOKEN", tokenInterno)
	org, url := tenantcelltest.New(t, tokenInterno, cells)
	m, err := tenantcell.NewMembership("pe-01", tenantcell.NewResolver(url, tokenInterno, zap.NewNop()), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	return org, apiRouter(nil, m, routes, zap.NewNop())
}

// rutasReales son las rutas del servicio sin casos de uso: una peticion que llegara a un
// handler que lee o escribe entraria en panico y romperia la prueba.
func rutasReales() http.Handler {
	return handler.NewHandler(nil, nil, nil, nil, authz.NewChecker("http://127.0.0.1:9", "")).Routes()
}

type llamada struct {
	method, path, tenant, user string
	n                          int
}

func pedir(h http.Handler, c llamada) *httptest.ResponseRecorder {
	req := httptest.NewRequest(c.method, c.path, strings.NewReader(`{}`))
	// Una IP por peticion: los limitadores del servicio no deben decidir el resultado.
	req.RemoteAddr = fmt.Sprintf("198.51.100.%d:4000", c.n%250+1)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gateway-Token", tokenInterno)
	if c.tenant != "" {
		req.Header.Set("X-Tenant-ID", c.tenant)
	}
	if c.user != "" {
		req.Header.Set("X-User-ID", c.user)
		req.Header.Set("X-User-Roles", "tenant_admin")
	}
	rec := httptest.NewRecorder()
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

// Cada ruta del API de administracion y las internas de DKIM (domain-service, con
// X-Tenant-ID) rechazan a una empresa de otra celda y a una desconocida sin llegar a ningun
// handler.
func TestCadaRutaRechazaUnaEmpresaQueNoEsDeLaCelda(t *testing.T) {
	ajena, nadie := uuid.NewString(), uuid.NewString()
	org, router := celdaPe01(t, map[string]string{ajena: "pe-02"}, rutasReales())

	var rutas []llamada
	err := chi.Walk(rutasReales().(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		rutas = append(rutas, llamada{method: method, path: strings.TrimSuffix(paramRe.ReplaceAllString(route, "x"), "/*")})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	vistas := map[string]bool{}
	n := 0
	for _, ruta := range rutas {
		vistas[strings.TrimSuffix(ruta.path, "/")] = true
		for _, quien := range []llamada{{tenant: ajena, user: uuid.NewString()}, {tenant: ajena}, {tenant: nadie, user: uuid.NewString()}} {
			n++
			c := llamada{method: ruta.method, path: ruta.path, tenant: quien.tenant, user: quien.user, n: n}
			if ruta.path == handler.SpamCheckPath && quien.user == "" {
				// Sin datos de empresa: la cubre TestLaPuntuacionAntispamNoPreguntaLaCelda.
				continue
			}
			if rec := pedir(router, c); rec.Code != http.StatusForbidden || codigoDeError(rec) != tenantcell.CodeNotInCell {
				t.Fatalf("%s %s como %+v: %d %s", c.method, c.path, quien, rec.Code, rec.Body)
			}
		}
	}
	for _, imprescindible := range []string{"/api/v1/mail-security/quarantine-settings", "/api/v1/mail-security/quarantine/x/release", "/api/v1/mail-security/firewall/networks", "/internal/mail-security/dkim/x"} {
		if !vistas[imprescindible] {
			t.Fatalf("la prueba no recorrio %s (%d rutas)", imprescindible, len(rutas))
		}
	}
	if org.Calls() != 2 {
		t.Fatalf("consultas a organization: %d", org.Calls())
	}
}

// La puntuacion antispam la pide templates, sin usuario, para empresas de cualquier celda: no pregunta a
// organization y llega a su handler aunque la empresa sea de otra celda o desconocida. Con usuario sigue
// el filtro de celda, y sin el token interno no pasa.
func TestLaPuntuacionAntispamNoPreguntaLaCelda(t *testing.T) {
	ajena := uuid.NewString()
	org, router := celdaPe01(t, map[string]string{ajena: "pe-02"}, rutasReales())
	for i, tenant := range []string{"", ajena, uuid.NewString()} {
		rec := pedir(router, llamada{method: http.MethodPost, path: handler.SpamCheckPath, tenant: tenant, n: i})
		if rec.Code != http.StatusBadRequest || codigoDeError(rec) != "MESSAGE_REQUIRED" {
			t.Fatalf("empresa %q: %d %s", tenant, rec.Code, rec.Body)
		}
	}
	if org.Calls() != 0 {
		t.Fatalf("consultas a organization: %d", org.Calls())
	}
	rec := pedir(router, llamada{method: http.MethodPost, path: handler.SpamCheckPath, tenant: ajena, user: uuid.NewString(), n: 9})
	if rec.Code != http.StatusForbidden || codigoDeError(rec) != tenantcell.CodeNotInCell {
		t.Fatalf("con usuario: %d %s", rec.Code, rec.Body)
	}
	req := httptest.NewRequest(http.MethodPost, handler.SpamCheckPath, strings.NewReader(`{}`))
	sinToken := httptest.NewRecorder()
	router.ServeHTTP(sinToken, req)
	if sinToken.Code != http.StatusUnauthorized && sinToken.Code != http.StatusForbidden {
		t.Fatalf("sin token interno: %d %s", sinToken.Code, sinToken.Body)
	}
}

func TestLaEmpresaDeLaCeldaLlegaASusRutas(t *testing.T) {
	socia, nueva := uuid.NewString(), uuid.NewString()
	org, router := celdaPe01(t, map[string]string{socia: "pe-01", nueva: "pe-01"}, rutasReales())
	salud := llamada{method: http.MethodGet, path: "/api/v1/mail-security/health", tenant: socia, user: uuid.NewString()}
	if rec := pedir(router, salud); rec.Code != http.StatusOK {
		t.Fatalf("empresa de la celda: %d %s", rec.Code, rec.Body)
	}
	org.SetDown(true)
	if rec := pedir(router, salud); rec.Code != http.StatusOK {
		t.Fatalf("empresa comprobada con organization caido: %d %s", rec.Code, rec.Body)
	}
	salud.tenant = nueva
	if rec := pedir(router, salud); rec.Code != http.StatusServiceUnavailable || codigoDeError(rec) != tenantcell.CodeCellUnavailable {
		t.Fatalf("empresa sin comprobar con organization caido: %d %s", rec.Code, rec.Body)
	}
}

// rutasConCortafuegos son las rutas del servicio con los casos de uso del cortafuegos y de la cola sobre
// dobles en memoria; el resto sin casos de uso, de modo que atender una ruta que no es de plataforma
// entraria en panico.
func rutasConCortafuegos() chi.Router {
	policy := apptest.NewPolicyReader()
	policy.FirewallNets = []domain.FirewallNetwork{{ID: uuid.New(), List: "deny", Network: "192.0.2.0/24", Note: "red de pe-01"}}
	fw := app.NewFirewallUseCase(app.FirewallDeps{Policy: policy, Store: apptest.NewStore(), Logger: zap.NewNop()})
	queue := app.NewQueueUseCase(apptest.NewQueue(), zap.NewNop())
	antispam := app.NewAntispamUseCase(apptest.NewAntispam(), zap.NewNop())
	return handler.NewHandler(nil, nil, fw, nil, authz.NewChecker("http://127.0.0.1:9", "")).WithQueue(queue).WithAntispam(antispam).Routes()
}

func pedirConCeldaDestino(h http.Handler, method, path, tenant, roles, cell string, n int) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(`{`))
	req.RemoteAddr = fmt.Sprintf("203.0.113.%d:4000", n%250+1)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gateway-Token", tokenInterno)
	req.Header.Set("X-Tenant-ID", tenant)
	req.Header.Set("X-User-ID", uuid.NewString())
	req.Header.Set("X-User-Roles", roles)
	if cell != "" {
		req.Header.Set("X-Operator-Cell", cell)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// El superadmin, cuya empresa vive en otra celda, opera el cortafuegos de esta con celda destino:
// cada ruta del cortafuegos le atiende (las lecturas responden, las escrituras llegan al handler,
// que rechaza el cuerpo) y cualquier otra ruta del servicio, de datos de empresa, publica o
// interna, le responde 403 sin llegar a su handler. Sin celda destino sigue rechazado por ser de
// otra celda; un administrador de empresa no usa la celda destino.
func TestElOperadorConCeldaDestinoSoloLlegaAlCortafuegos(t *testing.T) {
	plataforma, socia := uuid.NewString(), uuid.NewString()
	t.Setenv("INTERNAL_GATEWAY_TOKEN", tokenInterno)
	org, url := tenantcelltest.New(t, tokenInterno, map[string]string{plataforma: "pe-02", socia: "pe-01"})
	m, err := tenantcell.NewMembership("pe-01", tenantcell.NewResolver(url, tokenInterno, zap.NewNop()), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	routes := rutasConCortafuegos()
	if err := m.AcceptOperators(routes, handler.PlatformRoutes()); err != nil {
		t.Fatal(err)
	}
	router := apiRouter(nil, m, routes, zap.NewNop())

	plataformaRutas := map[string]bool{}
	for _, pr := range handler.PlatformRoutes() {
		plataformaRutas[pr.Method+" "+pr.Pattern] = true
	}
	if len(plataformaRutas) != 15 {
		t.Fatalf("rutas de plataforma: %v", plataformaRutas)
	}
	n, atendidas, rechazadas := 0, 0, 0
	err = chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		n++
		path := strings.TrimSuffix(paramRe.ReplaceAllString(route, "x"), "/*")
		rec := pedirConCeldaDestino(router, method, path, plataforma, "superadmin", "pe-01", n)
		switch {
		case !plataformaRutas[method+" "+route]:
			rechazadas++
			if rec.Code != http.StatusForbidden || codigoDeError(rec) != tenantcell.CodePlatformScopeOnly {
				t.Fatalf("%s %s con celda destino: %d %s", method, route, rec.Code, rec.Body)
			}
		case method == http.MethodGet:
			atendidas++
			if rec.Code != http.StatusOK {
				t.Fatalf("%s %s: %d %s", method, route, rec.Code, rec.Body)
			}
		default:
			atendidas++
			// El cuerpo roto llega al handler: 400 en el cortafuegos; en la cola no hay cuerpo, asi que
			// vaciar responde 202 y una accion sobre el identificador "x" el 422 de su validacion.
			want := http.StatusBadRequest
			switch {
			case strings.HasSuffix(route, "/queue/flush"):
				want = http.StatusAccepted
			case strings.Contains(route, "/queue/{id}"):
				want = http.StatusUnprocessableEntity
			}
			if rec.Code != want {
				t.Fatalf("%s %s: %d %s, se esperaba %d del handler", method, route, rec.Code, rec.Body, want)
			}
		}
		return nil
	})
	if err != nil || atendidas != 15 || rechazadas < 30 {
		t.Fatalf("atendidas %d, rechazadas %d: %v", atendidas, rechazadas, err)
	}
	firewall := "/api/v1/mail-security/firewall/networks"
	if rec := pedirConCeldaDestino(router, http.MethodGet, firewall, plataforma, "superadmin", "pe-01", 1); !strings.Contains(rec.Body.String(), "192.0.2.0/24") {
		t.Fatalf("cortafuegos de la celda: %s", rec.Body)
	}
	if rec := pedirConCeldaDestino(router, http.MethodGet, firewall, plataforma, "superadmin", "pe-02", 2); rec.Code != http.StatusForbidden ||
		codigoDeError(rec) != tenantcell.CodeTargetCellMismatch {
		t.Fatalf("celda destino de otra celda: %d %s", rec.Code, rec.Body)
	}
	if rec := pedirConCeldaDestino(router, http.MethodGet, firewall, socia, "tenant_admin", "pe-01", 3); rec.Code != http.StatusForbidden ||
		codigoDeError(rec) != tenantcell.CodePlatformScopeOnly {
		t.Fatalf("administrador de empresa con celda destino: %d %s", rec.Code, rec.Body)
	}
	if org.Calls() != 0 {
		t.Fatalf("la celda destino pregunto %d veces a organization", org.Calls())
	}
	if rec := pedirConCeldaDestino(router, http.MethodGet, firewall, plataforma, "superadmin", "", 4); rec.Code != http.StatusForbidden ||
		codigoDeError(rec) != tenantcell.CodeNotInCell {
		t.Fatalf("superadmin de otra celda sin celda destino: %d %s", rec.Code, rec.Body)
	}
}

// Los enlaces publicos de cuarentena no llevan empresa del gateway: su celda va en la firma y
// la comprueba el caso de uso. Pasan sin preguntar a organization y responden su propia pagina.
func TestLosEnlacesPublicosNoPreguntanLaCelda(t *testing.T) {
	org, router := celdaPe01(t, nil, rutasReales())
	rec := pedir(router, llamada{method: http.MethodGet, path: "/api/v1/public/mail-security/quarantine/pe-01/release"})
	if rec.Code != http.StatusForbidden || codigoDeError(rec) != "" || !strings.Contains(rec.Body.String(), "<") || org.Calls() != 0 {
		t.Fatalf("enlace publico: %d %q, consultas %d", rec.Code, rec.Body, org.Calls())
	}
}

// rutasConCola son las rutas del servicio con la cola de Postfix sobre un doble; queue nil las deja sin
// gestor (sin QUEUE_AGENT_API_KEY).
func rutasConCola(engine *apptest.Queue) http.Handler {
	h := handler.NewHandler(nil, nil, nil, nil, authz.NewChecker("http://127.0.0.1:9", ""))
	if engine != nil {
		h.WithQueue(app.NewQueueUseCase(engine, zap.NewNop()))
	}
	return h.Routes()
}

// La cola de Postfix mezcla el correo de todas las empresas de la celda: la ve y la cambia el
// superadmin y nadie mas, y solo con entradas validas.
func TestLaColaSoloLaOperaElSuperadminYSoloConEntradasValidas(t *testing.T) {
	empresa := uuid.NewString()
	engine := apptest.NewQueue()
	engine.Listing = domain.QueueListing{Total: 1, Items: []domain.QueueMessage{{QueueID: "ABCDEF1234", Sender: "a@b.c"}}}
	_, router := celdaPe01(t, map[string]string{empresa: "pe-01"}, rutasConCola(engine))
	n := 0
	pedirCola := func(method, path, roles string) *httptest.ResponseRecorder {
		n++
		return pedirConCeldaDestino(router, method, path, empresa, roles, "", n)
	}
	const base = "/api/v1/mail-security/queue"

	if rec := pedirCola(http.MethodGet, base+"?limit=7", "superadmin"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ABCDEF1234") || engine.ListLimit != 7 {
		t.Fatalf("listar: %d %s limit=%d", rec.Code, rec.Body, engine.ListLimit)
	}
	for _, bad := range []string{"0", "-1", "x"} {
		if rec := pedirCola(http.MethodGet, base+"?limit="+bad, "superadmin"); rec.Code != http.StatusBadRequest {
			t.Errorf("limit=%s: %d", bad, rec.Code)
		}
	}
	for _, a := range []struct{ method, path, want string }{
		{http.MethodPost, base + "/ABCDEF1234/retry", "retry ABCDEF1234"},
		{http.MethodPost, base + "/ABCDEF1234/hold", "hold ABCDEF1234"},
		{http.MethodPost, base + "/ABCDEF1234/unhold", "unhold ABCDEF1234"},
		{http.MethodDelete, base + "/ABCDEF1234", "delete ABCDEF1234"},
	} {
		before := len(engine.Applied)
		if rec := pedirCola(a.method, a.path, "superadmin"); rec.Code != http.StatusNoContent {
			t.Fatalf("%s %s: %d %s", a.method, a.path, rec.Code, rec.Body)
		}
		if len(engine.Applied) != before+1 || engine.Applied[before] != a.want {
			t.Fatalf("%s %s aplico %v, se esperaba %q", a.method, a.path, engine.Applied, a.want)
		}
	}
	if rec := pedirCola(http.MethodPost, base+"/flush", "superadmin"); rec.Code != http.StatusAccepted || engine.Flushed != 1 {
		t.Fatalf("vaciar: %d flush=%d", rec.Code, engine.Flushed)
	}

	// Entradas invalidas: 422 y el motor no se entera.
	aplicadas := len(engine.Applied)
	for _, id := range []string{"x", "ABC", "ABCDEF-1234", "ABCDEF1234%3Breboot"} {
		if rec := pedirCola(http.MethodDelete, base+"/"+id, "superadmin"); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("identificador %q: %d %s", id, rec.Code, rec.Body)
		}
	}
	if len(engine.Applied) != aplicadas {
		t.Fatalf("una entrada invalida llego al motor: %v", engine.Applied[aplicadas:])
	}

	// Un administrador de empresa no llega al motor, con ninguna de las rutas.
	engine.ListLimit, engine.Flushed, aplicadas = 0, 0, len(engine.Applied)
	for _, r := range [][2]string{
		{http.MethodGet, base}, {http.MethodPost, base + "/flush"}, {http.MethodPost, base + "/ABCDEF1234/retry"},
		{http.MethodPost, base + "/ABCDEF1234/hold"}, {http.MethodPost, base + "/ABCDEF1234/unhold"}, {http.MethodDelete, base + "/ABCDEF1234"},
	} {
		if rec := pedirCola(r[0], r[1], "tenant_admin"); rec.Code < 400 {
			t.Errorf("%s %s como tenant_admin: %d", r[0], r[1], rec.Code)
		}
	}
	if engine.ListLimit != 0 || engine.Flushed != 0 || len(engine.Applied) != aplicadas {
		t.Fatalf("un administrador de empresa llego al motor: %+v", engine)
	}
}

func TestSinAgenteLaColaResponde503YElRestoDelServicioSigue(t *testing.T) {
	empresa := uuid.NewString()
	_, router := celdaPe01(t, map[string]string{empresa: "pe-01"}, rutasConCola(nil))
	rec := pedirConCeldaDestino(router, http.MethodGet, "/api/v1/mail-security/queue", empresa, "superadmin", "", 1)
	if rec.Code != http.StatusServiceUnavailable || codigoDeError(rec) != "NOT_CONFIGURED" {
		t.Fatalf("cola sin agente: %d %s", rec.Code, rec.Body)
	}
	if rec := pedir(router, llamada{method: http.MethodGet, path: "/api/v1/mail-security/health", tenant: empresa, user: uuid.NewString()}); rec.Code != http.StatusOK {
		t.Fatalf("el resto del servicio: %d", rec.Code)
	}
}
