package maildirectorycli

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/tenantcell"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type recibido struct {
	instancia, metodo, ruta, empresa, token string
}

type registro struct {
	mu        sync.Mutex
	recibidos []recibido
}

func (g *registro) instancia(t *testing.T, nombre string, status int, cuerpo string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.recibidos = append(g.recibidos, recibido{nombre, r.Method, r.URL.Path, r.Header.Get("X-Tenant-ID"), r.Header.Get("X-Gateway-Token")})
		g.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, cuerpo)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func (g *registro) tomar() []recibido {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := g.recibidos
	g.recibidos = nil
	return out
}

// cliente monta el cliente con la celda base pe-01 y una instancia en pe-02, las dos con la
// respuesta indicada; pe-03 no tiene instancia.
func cliente(t *testing.T, status int, cuerpo string) (*Client, *registro) {
	t.Helper()
	g := &registro{}
	base, pe02 := g.instancia(t, "pe-01", status, cuerpo), g.instancia(t, "pe-02", status, cuerpo)
	inst := tenantcell.Instances{BaseCell: "pe-01", ByEnv: map[string]map[string]string{"MAIL_DIRECTORY_CELL_HOSTS": {"pe-02": pe02}}}
	targets, err := inst.CellTargets("MAIL_DIRECTORY_CELL_HOSTS", base)
	if err != nil {
		t.Fatal(err)
	}
	caller := tenantcell.NewCaller("mail-directory-baja", targets, "token-interno", zap.NewNop(), tenantcell.CallerOptions{})
	return New(caller), g
}

// La baja va a la instancia de la celda de la empresa, con la empresa y el token interno, y una
// celda sin instancia declarada no sale hacia ninguna.
func TestLaBajaVaALaInstanciaDeLaCeldaDeLaEmpresa(t *testing.T) {
	c, g := cliente(t, http.StatusOK, `{"data":{}}`)
	tenant := uuid.New()
	ctx := context.Background()
	for _, cell := range []string{"pe-02", "pe-01"} {
		if err := c.RetireTenant(ctx, cell, tenant); err != nil {
			t.Fatalf("%s: %v", cell, err)
		}
		want := recibido{cell, http.MethodPut, "/internal/mail-directory/tenant-retirement", tenant.String(), "token-interno"}
		if got := g.tomar(); len(got) != 1 || got[0] != want {
			t.Fatalf("%s: %+v, se esperaba %+v", cell, got, want)
		}
	}
	if err := c.RetireTenant(ctx, "pe-03", tenant); !errors.Is(err, tenantcell.ErrNotServed) {
		t.Fatalf("celda sin instancia: %v", err)
	}
	if got := g.tomar(); len(got) != 0 {
		t.Fatalf("salio hacia una instancia: %+v", got)
	}
}

// Solo un 200 es la baja hecha: una instancia que no sirve la ruta (404), una de otra celda
// (403 TENANT_NOT_IN_CELL) o cualquier fallo dejan el paso sin hacer.
func TestSoloUn200EsLaBajaHecha(t *testing.T) {
	for nombre, c := range map[string]struct {
		status   int
		cuerpo   string
		notInCel bool
	}{
		"ruta que no se sirve":    {http.StatusNotFound, "404 page not found", false},
		"instancia de otra celda": {http.StatusForbidden, `{"error":{"code":"TENANT_NOT_IN_CELL","message":"x"}}`, true},
		"con usuario":             {http.StatusForbidden, `{"error":{"code":"FORBIDDEN","message":"x"}}`, false},
		"error interno":           {http.StatusInternalServerError, `{"error":{"code":"INTERNAL_ERROR","message":"x"}}`, false},
		"sin contenido":           {http.StatusNoContent, "", false},
	} {
		cl, _ := cliente(t, c.status, c.cuerpo)
		err := cl.RetireTenant(context.Background(), "pe-01", uuid.New())
		if err == nil || errors.Is(err, tenantcell.ErrNotInCell) != c.notInCel {
			t.Errorf("%s: %v", nombre, err)
		}
	}
}
