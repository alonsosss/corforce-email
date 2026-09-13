package organizationcli

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/alonsosss/corforce-email/services/domain-service/internal/domain"
	"github.com/google/uuid"
)

// organizationDePrueba responde a cada llamada lo que la prueba le pide y anota metodo y ruta.
type organizationDePrueba struct {
	mu      sync.Mutex
	calls   []string
	replies []reply
	t       *testing.T
}

type reply struct {
	status int
	body   string
}

func (o *organizationDePrueba) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if r.Header.Get("X-Gateway-Token") != "token-interno" || r.Header.Get("X-User-ID") != "" || r.Header.Get("X-Tenant-ID") != "" {
		o.t.Errorf("cabeceras: token %q usuario %q empresa %q", r.Header.Get("X-Gateway-Token"), r.Header.Get("X-User-ID"), r.Header.Get("X-Tenant-ID"))
	}
	o.calls = append(o.calls, r.Method+" "+r.URL.EscapedPath())
	next := reply{status: http.StatusInternalServerError}
	if len(o.replies) > 0 {
		next, o.replies = o.replies[0], o.replies[1:]
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(next.status)
	_, _ = w.Write([]byte(next.body))
}

func nuevoCliente(t *testing.T, replies ...reply) (*Client, *organizationDePrueba) {
	t.Helper()
	org := &organizationDePrueba{replies: replies, t: t}
	srv := httptest.NewServer(org)
	t.Cleanup(srv.Close)
	return New(srv.URL+"/", "token-interno"), org
}

func TestReclamoYRetiradaDeUnDominio(t *testing.T) {
	tenant := uuid.New()
	ruta := "/internal/organization/tenants/" + tenant.String() + "/mail-domains/beta.test"
	ctx := context.Background()

	c, org := nuevoCliente(t,
		reply{http.StatusOK, `{"data":{"domain":"beta.test","tenant_id":"` + tenant.String() + `","cell_code":"pe-02"}}`},
		reply{http.StatusNoContent, ""},
	)
	if err := c.Claim(ctx, tenant, "beta.test"); err != nil {
		t.Fatalf("reclamo: %v", err)
	}
	if err := c.Release(ctx, tenant, "beta.test"); err != nil {
		t.Fatalf("retirada: %v", err)
	}
	if len(org.calls) != 2 || org.calls[0] != "PUT "+ruta || org.calls[1] != "DELETE "+ruta {
		t.Fatalf("llamadas: %v", org.calls)
	}
}

// Solo el 409 MAIL_DOMAIN_CLAIMED es un dominio de otra empresa; ninguna otra respuesta cuenta como
// hecho y un 503 se reintenta.
func TestRespuestasDeOrganization(t *testing.T) {
	tenant := uuid.New()
	ctx := context.Background()
	for nombre, c := range map[string]struct {
		replies []reply
		claimed bool
		ok      bool
	}{
		"de otra empresa":      {replies: []reply{{http.StatusConflict, `{"error":{"code":"MAIL_DOMAIN_CLAIMED","message":"x"}}`}}, claimed: true},
		"409 con otro codigo":  {replies: []reply{{http.StatusConflict, `{"error":{"code":"OTRO","message":"x"}}`}}},
		"empresa desconocida":  {replies: []reply{{http.StatusNotFound, `{"error":{"code":"TENANT_NOT_FOUND","message":"x"}}`}}},
		"ruta que no se sirve": {replies: []reply{{http.StatusNotFound, `404 page not found`}}},
		"error interno":        {replies: []reply{{http.StatusInternalServerError, `{}`}}},
		"503 y despues bien":   {replies: []reply{{http.StatusServiceUnavailable, `{}`}, {http.StatusOK, `{}`}}, ok: true},
	} {
		cl, _ := nuevoCliente(t, c.replies...)
		err := cl.Claim(ctx, tenant, "beta.test")
		switch {
		case c.ok && err != nil:
			t.Errorf("%s: %v", nombre, err)
		case !c.ok && err == nil:
			t.Errorf("%s: se dio por reclamado", nombre)
		case errors.Is(err, domain.ErrDomainClaimedElsewhere) != c.claimed:
			t.Errorf("%s: %v", nombre, err)
		}
	}

	cl, _ := nuevoCliente(t, reply{http.StatusOK, `{}`})
	if err := cl.Release(ctx, tenant, "beta.test"); err == nil {
		t.Error("una retirada solo es hecha con 204")
	}
}
