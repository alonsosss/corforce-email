package reputationclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
)

func TestAuthorizeContract(t *testing.T) {
	tenant := uuid.New()
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/internal/reputation/authorize" {
			t.Errorf("ruta %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("X-Gateway-Token") != "tok" || r.Header.Get("X-Tenant-ID") != tenant.String() {
			t.Errorf("cabeceras internas ausentes: %v", r.Header)
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"data":{"allowed":true,"class":"marketing","state":"healthy"}}`))
	}))
	defer srv.Close()

	auth, err := New(srv.URL+"/", "tok").Authorize(context.Background(), tenant, "marketing", 42)
	if err != nil || !auth.Allowed || auth.Class != "marketing" || auth.State != "healthy" || auth.RetryAfterSeconds != nil {
		t.Fatalf("Authorize: %+v %v", auth, err)
	}
	if body["class"] != "marketing" || body["count"] != float64(42) {
		t.Fatalf("cuerpo enviado: %v", body)
	}
}

// reputation deniega con 200 y el mismo cuerpo que al autorizar (hourly, daily, monthly).
func TestAuthorizeDenials(t *testing.T) {
	reply := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(reply))
	}))
	defer srv.Close()
	c := New(srv.URL, "tok")

	reply = `{"data":{"allowed":false,"class":"marketing","state":"ok","reason":"rate_limited","retry_after_seconds":12.2,` +
		`"hourly":{"limit":5000,"used":5000},"daily":{"limit":50000,"used":9000},"monthly":null}}`
	auth, err := c.Authorize(context.Background(), uuid.New(), "marketing", 1)
	if err != nil || auth.Allowed || auth.Reason != "rate_limited" || auth.RetryAfterSeconds == nil || *auth.RetryAfterSeconds != 13 {
		t.Fatalf("una denegacion es una respuesta, no un error, y la espera se redondea hacia arriba: %+v %v", auth, err)
	}

	reply = `{"data":{"allowed":false,"class":"marketing","state":"ok","reason":"rate_limited","hourly":{"limit":10,"used":0},"daily":{"limit":100,"used":0},"monthly":null}}`
	if auth, err := c.Authorize(context.Background(), uuid.New(), "marketing", 500); err != nil || auth.RetryAfterSeconds != nil {
		t.Fatalf("sin retry_after_seconds no hay espera que valga (el lote no cabe en la ventana): %+v %v", auth, err)
	}

	reply = `{"data":{"allowed":false,"class":"transactional","state":"suspended","reason":"suspended","hourly":{"limit":10,"used":null},"daily":{"limit":100,"used":null},"monthly":null}}`
	if auth, err := c.Authorize(context.Background(), uuid.New(), "transactional", 1); err != nil || auth.Allowed || auth.State != "suspended" {
		t.Fatalf("suspension: %+v %v", auth, err)
	}
}

func TestAuthorizeNoAnswerIsError(t *testing.T) {
	var calls atomic.Int32
	status, reply := http.StatusServiceUnavailable, `{}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	defer srv.Close()
	c := New(srv.URL, "tok")

	if _, err := c.Authorize(context.Background(), uuid.New(), "marketing", 1); err == nil {
		t.Fatal("un 5xx es no responder")
	}
	if calls.Load() != 1 {
		t.Fatalf("la autorizacion no se repite (puede descontar cupo): %d llamadas", calls.Load())
	}
	for name, r := range map[string]string{
		"sin allowed":  `{"data":{"class":"marketing"}}`,
		"sin data":     `{"error":{"code":"X"}}`,
		"no es JSON":   `<html>`,
		"allowed nulo": `{"data":{"allowed":null}}`,
	} {
		status, reply = http.StatusOK, r
		if _, err := c.Authorize(context.Background(), uuid.New(), "marketing", 1); err == nil {
			t.Errorf("%s: una respuesta sin decision es un error", name)
		}
	}
	status, reply = http.StatusUnprocessableEntity, `{"error":{"code":"VALIDATION_ERROR"}}`
	if _, err := c.Authorize(context.Background(), uuid.New(), "marketing", 1); err == nil {
		t.Fatal("un 4xx no es una autorizacion")
	}
	if _, err := New("", "tok").Authorize(context.Background(), uuid.New(), "marketing", 1); err == nil {
		t.Fatal("sin REPUTATION_URL es un error")
	}
}

func TestRetryAfterBounds(t *testing.T) {
	for in, want := range map[float64]int{-5: 0, 0: 0, 0.1: 1, 30: 30, 1e12: maxRetryAfterSeconds} {
		if got := retryAfter(in); got != want {
			t.Errorf("retryAfter(%v) = %d, se esperaba %d", in, got, want)
		}
	}
}
