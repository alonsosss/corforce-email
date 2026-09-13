// Package tenantcelltest sirve en las pruebas la API interna de organization que consulta
// tenantcell.Resolver.
package tenantcelltest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Organization hace de GET /internal/organization/tenants/{id}/cell y de
// GET /internal/organization/mail-domains/{dominio}/cell: sirve la celda de las empresas y de
// los dominios que conoce, 404 TENANT_NOT_FOUND o MAIL_DOMAIN_NOT_FOUND para los demas y,
// caida, 500. Falla la prueba si la consulta no llega con el token interno o si trae usuario
// (RequireInternalCaller).
type Organization struct {
	t     testing.TB
	token string

	mu          sync.Mutex
	cells       map[string]string
	domains     map[string]string
	down        bool
	raw         string
	calls       int
	domainCalls int
	release     chan struct{}
}

// New arranca la organization de prueba con las celdas dadas (empresa -> celda) y devuelve su
// URL. Se cierra al terminar la prueba.
func New(t testing.TB, token string, cells map[string]string) (*Organization, string) {
	t.Helper()
	if cells == nil {
		cells = map[string]string{}
	}
	o := &Organization{t: t, token: token, cells: cells, domains: map[string]string{}}
	srv := httptest.NewServer(o)
	t.Cleanup(srv.Close)
	return o, srv.URL
}

func (o *Organization) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	o.mu.Lock()
	o.calls++
	if strings.HasPrefix(r.URL.Path, "/internal/organization/mail-domains/") {
		o.domainCalls++
	}
	down, raw, release := o.down, o.raw, o.release
	o.mu.Unlock()
	if release != nil {
		<-release
	}
	if r.Header.Get("X-Gateway-Token") != o.token || r.Header.Get("X-User-ID") != "" {
		o.t.Errorf("organization recibio token %q y usuario %q", r.Header.Get("X-Gateway-Token"), r.Header.Get("X-User-ID"))
	}
	w.Header().Set("Content-Type", "application/json")
	key, field, table, notFound, ok := o.route(r.URL.Path)
	switch {
	case !ok:
		http.NotFound(w, r)
	case down:
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"code":"INTERNAL_ERROR","message":"x"}}`))
	case raw != "":
		_, _ = w.Write([]byte(raw))
	default:
		o.mu.Lock()
		cell, known := table[key]
		o.mu.Unlock()
		if !known {
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprintf(w, `{"error":{"code":%q,"message":"no encontrado"}}`, notFound)
			return
		}
		_, _ = fmt.Fprintf(w, `{"data":{%q:%q,"cell_code":%q}}`, field, key, cell)
	}
}

// route reconoce las dos rutas de celda y devuelve la clave, el campo que la repite, la tabla
// donde buscarla y el codigo de su negativa.
func (o *Organization) route(path string) (key, field string, table map[string]string, notFound string, ok bool) {
	if rest, isTenant := strings.CutPrefix(path, "/internal/organization/tenants/"); isTenant {
		key, ok = strings.CutSuffix(rest, "/cell")
		return key, "tenant_id", o.cells, "TENANT_NOT_FOUND", ok && key != "" && !strings.Contains(key, "/")
	}
	if rest, isDomain := strings.CutPrefix(path, "/internal/organization/mail-domains/"); isDomain {
		key, ok = strings.CutSuffix(rest, "/cell")
		return key, "domain", o.domains, "MAIL_DOMAIN_NOT_FOUND", ok && key != "" && !strings.Contains(key, "/")
	}
	return "", "", nil, "", false
}

// SetCell da de alta (o cambia) la celda de una empresa; cell vacia la borra.
func (o *Organization) SetCell(tenant, cell string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if cell == "" {
		delete(o.cells, tenant)
		return
	}
	o.cells[tenant] = cell
}

// SetDomain da de alta (o cambia) la celda de un dominio de correo; cell vacia lo borra.
func (o *Organization) SetDomain(domain, cell string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if cell == "" {
		delete(o.domains, domain)
		return
	}
	o.domains[domain] = cell
}

// SetDown apaga o enciende organization: apagada responde 500 a todo.
func (o *Organization) SetDown(down bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.down = down
}

// SetRaw hace que cada respuesta sea el cuerpo literal dado con 200.
func (o *Organization) SetRaw(raw string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.raw = raw
}

// Hold retiene cada respuesta hasta que se cierre release.
func (o *Organization) Hold(release chan struct{}) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.release = release
}

// Calls es el numero de consultas recibidas.
func (o *Organization) Calls() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.calls
}

// DomainCalls es el numero de consultas recibidas por la celda de un dominio.
func (o *Organization) DomainCalls() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.domainCalls
}
