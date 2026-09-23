package transactionalcli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/alonsosss/corforce-email/services/templates/internal/domain"
	"github.com/alonsosss/corforce-email/services/templates/internal/ports"
	"github.com/google/uuid"
)

func request() ports.TestSendRequest {
	return ports.TestSendRequest{
		TemplateID: uuid.New(), Version: 2, FromEmail: "hola@acme.test", FromName: "Acme",
		To: []string{"qa@example.com"}, Variables: map[string]json.RawMessage{"name": json.RawMessage(`"Ana"`)},
		RequestedBy: uuid.New(),
	}
}

func TestSendTestContract(t *testing.T) {
	tenant := uuid.New()
	req := request()
	messageID := uuid.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != testSendPath {
			t.Errorf("ruta: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("X-Gateway-Token") != "token" || r.Header.Get("X-Tenant-ID") != tenant.String() {
			t.Errorf("cabeceras internas: %v", r.Header)
		}
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		if body["template_id"] != req.TemplateID.String() || body["template_version"] != float64(2) ||
			body["requested_by"] != req.RequestedBy.String() || body["from"].(map[string]any)["email"] != "hola@acme.test" {
			t.Errorf("cuerpo: %s", raw)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"data":{"messages":[{"id":"`+messageID.String()+`","status":"queued","to":[{"email":"qa@example.com"}]}],`+
			`"suppressed":[{"email":"baja@example.com","reason":"unsubscribe"}]}}`)
	}))
	defer srv.Close()

	out, err := New(srv.URL+"/", "token").SendTest(context.Background(), tenant, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Messages) != 1 || out.Messages[0].ID != messageID || out.Messages[0].Email != "qa@example.com" ||
		len(out.Suppressed) != 1 || out.Suppressed[0].Reason != "unsubscribe" {
		t.Fatalf("resultado: %+v", out)
	}
}

func TestSendTestRelaysRejectionsAndNeverRetries(t *testing.T) {
	var calls atomic.Int32
	status, body := http.StatusUnprocessableEntity, `{"error":{"code":"SENDING_DOMAIN_NOT_VERIFIED","message":"sin verificar"}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()
	c := New(srv.URL, "token")

	_, err := c.SendTest(context.Background(), uuid.New(), request())
	var rejected *domain.TestSendRejectedError
	if !errors.As(err, &rejected) || rejected.Status != status || rejected.Code != "SENDING_DOMAIN_NOT_VERIFIED" || rejected.RetryAfter != "30" {
		t.Fatalf("rechazo: %v", err)
	}

	for _, tc := range []struct {
		status int
		body   string
	}{
		{http.StatusUnauthorized, `{"error":{"code":"UNAUTHORIZED","message":"token"}}`},
		{http.StatusBadGateway, `no es json`},
		{http.StatusServiceUnavailable, `no es json`},
	} {
		calls.Store(0)
		status, body = tc.status, tc.body
		if _, err := c.SendTest(context.Background(), uuid.New(), request()); !errors.Is(err, domain.ErrTestSendUnavailable) {
			t.Fatalf("%d: %v", tc.status, err)
		}
		if calls.Load() != 1 {
			t.Fatalf("%d: una prueba no se reintenta (%d llamadas)", tc.status, calls.Load())
		}
	}
}
