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

// Organization hace de GET /internal/organization/tenants/{id}/cell: sirve la celda de las
// empresas que conoce, 404 TENANT_NOT_FOUND para las demas y, caida, 500. Falla la prueba si
// la consulta no llega con el token interno o si trae usuario (RequireInternalCaller).
type Organization struct {
	t     testing.TB
	token string

	mu      sync.Mutex
	cells   map[string]string
	down    bool
	raw     string
	calls   int
	release chan struct{}
}

// New arranca la organization de prueba con las celdas dadas (empresa -> celda) y devuelve su
// URL. Se cierra al terminar la prueba.
func New(t testing.TB, token string, cells map[string]string) (*Organization, string) {
	t.Helper()
	if cells == nil {
		cells = map[string]string{}
	}
	o := &Organization{t: t, token: token, cells: cells}
	srv := httptest.NewServer(o)
	t.Cleanup(srv.Close)
	return o, srv.URL
}

func (o *Organization) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	o.mu.Lock()
	o.calls++
	down, raw, release := o.down, o.raw, o.release
	o.mu.Unlock()
	if release != nil {
		<-release
	}
	if r.Header.Get("X-Gateway-Token") != o.token || r.Header.Get("X-User-ID") != "" {
		o.t.Errorf("organization recibio token %q y usuario %q", r.Header.Get("X-Gateway-Token"), r.Header.Get("X-User-ID"))
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
		o.mu.Lock()
		cell, known := o.cells[tenant]
		o.mu.Unlock()
		if !known {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"TENANT_NOT_FOUND","message":"empresa no encontrada"}}`))
			return
		}
		_, _ = fmt.Fprintf(w, `{"data":{"tenant_id":%q,"cell_code":%q}}`, tenant, cell)
	}
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
