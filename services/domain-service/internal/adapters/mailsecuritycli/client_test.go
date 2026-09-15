package mailsecuritycli

import (
	"context"
	"encoding/json"
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

// El juego completo va en una sola llamada y en orden: la ultima clave es la que firma y
// mail-security retira los selectores que no vienen.
func TestPublicaElJuegoCompletoEnUnaLlamada(t *testing.T) {
	c, recibidos := nuevo(t, siempre(http.StatusNoContent, ""))
	tenant := uuid.New()
	keys := []ports.DKIMKey{{Selector: "cfm202608", PrivateKeyPEM: "A"}, {Selector: "cfm202609", PrivateKeyPEM: "B"}}
	if err := c.PublishDKIM(context.Background(), tenant, "acme.test", keys); err != nil {
		t.Fatal(err)
	}
	got := recibidos()
	if len(got) != 1 || got[0].metodo != http.MethodPut || got[0].ruta != "/internal/mail-security/dkim/acme.test" || got[0].empresa != tenant.String() {
		t.Fatalf("llamadas: %+v", got)
	}
	var cuerpo struct {
		Keys []struct {
			Selector      string `json:"selector"`
			PrivateKeyPEM string `json:"private_key_pem"`
		} `json:"keys"`
	}
	if err := json.Unmarshal([]byte(got[0].cuerpo), &cuerpo); err != nil {
		t.Fatal(err)
	}
	if len(cuerpo.Keys) != 2 || cuerpo.Keys[0].Selector != "cfm202608" || cuerpo.Keys[0].PrivateKeyPEM != "A" ||
		cuerpo.Keys[1].Selector != "cfm202609" || cuerpo.Keys[1].PrivateKeyPEM != "B" {
		t.Errorf("juego publicado: %+v", cuerpo.Keys)
	}
}

// Un dominio que la celda no sirve (409 DKIM_DOMAIN_NOT_ACTIVE) no es una publicacion hecha.
func TestUnJuegoRechazadoNoSeDaPorPublicado(t *testing.T) {
	c, _ := nuevo(t, siempre(http.StatusConflict, `{"error":{"code":"DKIM_DOMAIN_NOT_ACTIVE","message":"x"}}`))
	err := c.PublishDKIM(context.Background(), uuid.New(), "acme.test", []ports.DKIMKey{{Selector: "cfm202609", PrivateKeyPEM: "A"}})
	if err == nil || !strings.Contains(err.Error(), "409") {
		t.Fatalf("error: %v", err)
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
