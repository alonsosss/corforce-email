package schedulercli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/alonsosss/corforce-email/services/analytics/internal/ports"
	"github.com/google/uuid"
)

type captured struct {
	mu     sync.Mutex
	method string
	path   string
	token  string
	tenant string
	body   map[string]any
	calls  int
}

func (c *captured) snapshot() captured {
	c.mu.Lock()
	defer c.mu.Unlock()
	return captured{method: c.method, path: c.path, token: c.token, tenant: c.tenant, body: c.body, calls: c.calls}
}

// server responde status a toda peticion y guarda la ultima.
func server(t *testing.T, status int, body string) (*Client, *captured) {
	t.Helper()
	got := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		got.mu.Lock()
		got.method, got.path = r.Method, r.URL.Path
		got.token, got.tenant = r.Header.Get("X-Gateway-Token"), r.Header.Get("X-Tenant-ID")
		got.body = map[string]any{}
		_ = json.Unmarshal(raw, &got.body)
		got.calls++
		got.mu.Unlock()
		w.WriteHeader(status)
		if body != "" {
			_, _ = w.Write([]byte(body))
		}
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, "el-token-interno"), got
}

func TestCerrarConExitoLlevaLaEmpresaYElTokenInterno(t *testing.T) {
	client, got := server(t, http.StatusOK, `{"data":{}}`)
	tenant, execution := uuid.New(), uuid.New()

	if err := client.Complete(context.Background(), tenant, execution, map[string]int64{"messages": 3}); err != nil {
		t.Fatal(err)
	}

	c := got.snapshot()
	if c.method != http.MethodPost || c.path != "/internal/scheduler/executions/"+execution.String()+"/complete" {
		t.Fatalf("%s %s", c.method, c.path)
	}
	if c.token != "el-token-interno" || c.tenant != tenant.String() {
		t.Fatalf("token %q, empresa %q", c.token, c.tenant)
	}
	result, _ := c.body["result"].(map[string]any)
	if result["messages"] != float64(3) {
		t.Fatalf("el resultado viaja en el cuerpo: %v", c.body)
	}
}

func TestInformarUnFalloDiceSiEsReintentable(t *testing.T) {
	client, got := server(t, http.StatusOK, `{"data":{}}`)
	execution := uuid.New()

	if err := client.Fail(context.Background(), uuid.New(), execution, "la base no responde", true); err != nil {
		t.Fatal(err)
	}

	c := got.snapshot()
	if c.path != "/internal/scheduler/executions/"+execution.String()+"/fail" {
		t.Fatalf("ruta: %s", c.path)
	}
	if c.body["error"] != "la base no responde" || c.body["retryable"] != true {
		t.Fatalf("cuerpo: %v", c.body)
	}
}

// Un 4xx es definitivo (la ejecucion ya vencio, se cancelo o no es de esta empresa):
// repetirlo daria siempre lo mismo, asi que el consumidor confirma el mensaje.
func TestUn4xxEsUnRechazoDefinitivo(t *testing.T) {
	client, got := server(t, http.StatusConflict, `{"error":{"code":"CONFLICT"}}`)

	err := client.Complete(context.Background(), uuid.New(), uuid.New(), nil)

	if !errors.Is(err, ports.ErrReportRejected) {
		t.Fatalf("%v, se esperaba ErrReportRejected", err)
	}
	if c := got.snapshot(); c.calls != 1 {
		t.Fatalf("un 4xx no se repite: %d llamadas", c.calls)
	}
	if err != nil && !contains(err.Error(), "CONFLICT") {
		t.Fatalf("el motivo del scheduler queda en el error: %v", err)
	}
}

// Un 5xx es el scheduler caido: el cierre se vuelve a intentar.
func TestUn5xxDejaElCierreSinRegistrar(t *testing.T) {
	client, _ := server(t, http.StatusServiceUnavailable, `{"error":{"code":"UNAVAILABLE"}}`)

	err := client.Complete(context.Background(), uuid.New(), uuid.New(), nil)

	if !errors.Is(err, ports.ErrSchedulerUnavailable) {
		t.Fatalf("%v, se esperaba ErrSchedulerUnavailable", err)
	}
}

func TestSinURLDelSchedulerNoSeIntenta(t *testing.T) {
	err := New("", "el-token-interno").Complete(context.Background(), uuid.New(), uuid.New(), nil)
	if !errors.Is(err, ports.ErrSchedulerUnavailable) {
		t.Fatalf("%v, se esperaba ErrSchedulerUnavailable", err)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
