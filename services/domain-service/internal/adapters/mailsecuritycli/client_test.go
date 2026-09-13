package mailsecuritycli

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
	"github.com/alonsosss/corforce-email/services/domain-service/internal/adapters/cellcli"
	"github.com/alonsosss/corforce-email/services/domain-service/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type recibido struct {
	metodo, ruta, empresa, cuerpo string
}

// nuevo monta el cliente contra una instancia de una celda; responder decide la respuesta de
// cada llamada por su orden (0, 1, ...).
func nuevo(t *testing.T, responder func(n int, w http.ResponseWriter)) (*Client, func() []recibido) {
	t.Helper()
	var mu sync.Mutex
	var got []recibido
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		n := len(got)
		got = append(got, recibido{r.Method, r.URL.EscapedPath(), r.Header.Get("X-Tenant-ID"), string(b)})
		mu.Unlock()
		responder(n, w)
	}))
	t.Cleanup(srv.Close)
	targets, err := tenantcell.Instances{ByEnv: map[string]map[string]string{"X": {}}}.Targets("X", srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	return New(cellcli.New("mail-security", targets, "token-interno", zap.NewNop())), func() []recibido {
		mu.Lock()
		defer mu.Unlock()
		return append([]recibido(nil), got...)
	}
}

func siempre(status int, cuerpo string) func(int, http.ResponseWriter) {
	return func(_ int, w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, cuerpo)
	}
}

func TestPublicaLasClavesEnOrden(t *testing.T) {
	c, recibidos := nuevo(t, siempre(http.StatusNoContent, ""))
	tenant := uuid.New()
	keys := []ports.DKIMKey{{Selector: "cfm202608", PrivateKeyPEM: "A"}, {Selector: "cfm202609", PrivateKeyPEM: "B"}}
	if err := c.PublishDKIM(context.Background(), tenant, "acme.test", keys); err != nil {
		t.Fatal(err)
	}
	got := recibidos()
	if len(got) != 2 {
		t.Fatalf("llamadas: %+v", got)
	}
	for i, key := range keys {
		if got[i].metodo != http.MethodPut || got[i].ruta != "/internal/mail-security/dkim/acme.test" || got[i].empresa != tenant.String() ||
			!strings.Contains(got[i].cuerpo, `"selector":"`+key.Selector+`"`) {
			t.Errorf("llamada %d: %+v", i, got[i])
		}
	}
}

func TestUnaClaveQueNoSePublicaCortaLasSiguientes(t *testing.T) {
	c, recibidos := nuevo(t, func(n int, w http.ResponseWriter) {
		if n == 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	})
	keys := []ports.DKIMKey{{Selector: "cfm202608", PrivateKeyPEM: "A"}, {Selector: "cfm202609", PrivateKeyPEM: "B"}, {Selector: "cfm202610", PrivateKeyPEM: "C"}}
	err := c.PublishDKIM(context.Background(), uuid.New(), "acme.test", keys)
	if err == nil || !strings.Contains(err.Error(), "cfm202609") {
		t.Fatalf("error: %v", err)
	}
	if got := recibidos(); len(got) != 2 {
		t.Errorf("tras el fallo no se publica la siguiente: %d llamadas", len(got))
	}
}

func TestRetirarUnSelectorYTodas(t *testing.T) {
	c, recibidos := nuevo(t, siempre(http.StatusNoContent, ""))
	tenant := uuid.New()
	if err := c.RetireDKIM(context.Background(), tenant, "acme.test", "cfm202608"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteDKIM(context.Background(), tenant, "acme.test"); err != nil {
		t.Fatal(err)
	}
	got := recibidos()
	if len(got) != 2 || got[0].metodo != http.MethodDelete || got[0].ruta != "/internal/mail-security/dkim/acme.test/cfm202608" ||
		got[1].ruta != "/internal/mail-security/dkim/acme.test" {
		t.Errorf("llamadas: %+v", got)
	}
}

// Retirar una clave que no esta responde 204: un 404 es una instancia que no sirve la ruta y la
// clave podria seguir en los motores.
func TestUn404NoEsUnaClaveRetirada(t *testing.T) {
	c, _ := nuevo(t, siempre(http.StatusNotFound, "404 page not found"))
	if err := c.DeleteDKIM(context.Background(), uuid.New(), "acme.test"); err == nil {
		t.Error("DELETE con 404 se dio por hecho")
	}
	if err := c.RetireDKIM(context.Background(), uuid.New(), "acme.test", "cfm202608"); err == nil {
		t.Error("DELETE de un selector con 404 se dio por hecho")
	}
}

func TestInstanciaDeOtraCeldaEsErrorDeConfiguracion(t *testing.T) {
	c, _ := nuevo(t, siempre(http.StatusForbidden, `{"error":{"code":"TENANT_NOT_IN_CELL","message":"x"}}`))
	if err := c.DeleteDKIM(context.Background(), uuid.New(), "acme.test"); !errors.Is(err, cellcli.ErrNotInCell) {
		t.Errorf("DELETE: %v", err)
	}
	err := c.PublishDKIM(context.Background(), uuid.New(), "acme.test", []ports.DKIMKey{{Selector: "cfm202609", PrivateKeyPEM: "A"}})
	if !errors.Is(err, cellcli.ErrNotInCell) {
		t.Errorf("PUT: %v", err)
	}
}
