package internalapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/httpclient"
	"github.com/alonsosss/corforce-email/services/campaigns/internal/ports"
	"github.com/google/uuid"
)

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	cases := map[string]time.Duration{
		"120":    120 * time.Second,
		"":       0,
		"-5":     0,
		"pronto": 0,
		now.Add(90 * time.Second).Format(http.TimeFormat): 90 * time.Second,
		now.Add(-time.Minute).Format(http.TimeFormat):     0,
	}
	for in, want := range cases {
		if got := parseRetryAfter(in, now); got != want {
			t.Errorf("%q: %s, se esperaba %s", in, got, want)
		}
	}
}

func TestClassify(t *testing.T) {
	var limited *ports.RateLimitedError
	if err := Classify(&StatusError{Status: 429, RetryAfter: 30 * time.Second}); !errors.As(err, &limited) || limited.RetryAfter != 30*time.Second {
		t.Fatalf("429: %v", err)
	}
	var blocked *ports.BlockedError
	if err := Classify(&StatusError{Status: 403, Code: "SENDING_RESTRICTED", Message: "quejas"}); !errors.As(err, &blocked) || blocked.Code != "SENDING_RESTRICTED" {
		t.Fatalf("403: %v", err)
	}
	if err := Classify(&StatusError{Status: 403}); !errors.As(err, &blocked) || blocked.Code != "FORBIDDEN" {
		t.Fatalf("403 sin codigo: %v", err)
	}
	var rejected *ports.RejectedError
	if err := Classify(&StatusError{Status: 422, Message: "dominio no verificado"}); !errors.As(err, &rejected) || rejected.Message != "dominio no verificado" {
		t.Fatalf("422: %v", err)
	}
	err := Classify(&StatusError{Status: 422, Code: "TEMPLATE_MISSING_UNSUBSCRIBE", Message: "sin enlace de baja"})
	if !errors.As(err, &rejected) || rejected.Code != "TEMPLATE_MISSING_UNSUBSCRIBE" || err.Error() != "TEMPLATE_MISSING_UNSUBSCRIBE: sin enlace de baja" {
		t.Fatalf("422 con codigo: %v", err)
	}
	if err := Classify(&StatusError{Status: 503, Code: "TEMPLATES_UNAVAILABLE"}); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatalf("503 TEMPLATES_UNAVAILABLE es transitorio: %v", err)
	}
	if err := Classify(&StatusError{Status: 400}); !errors.As(err, &rejected) || rejected.Message == "" {
		t.Fatalf("400: %v", err)
	}
	for _, status := range []int{404, 409, 500, 503} {
		if err := Classify(&StatusError{Status: status}); !errors.Is(err, ports.ErrUnavailable) {
			t.Errorf("%d debe ser transitorio: %v", status, err)
		}
	}
	plain := errors.New("otro")
	if Classify(plain) != plain {
		t.Fatal("un error que no es de estado pasa tal cual")
	}
}

func TestPostSendsInternalHeadersAndParsesEnvelope(t *testing.T) {
	tenant := uuid.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Gateway-Token") != "secreto-de-prueba" || r.Header.Get("X-Tenant-ID") != tenant.String() ||
			r.Header.Get("Content-Type") != "application/json" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch r.URL.Path {
		case "/ok":
			_, _ = w.Write([]byte(`{"data":{"echo":"` + body["x"] + `"}}`))
		case "/conflict":
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":{"code":"CONFLICT","message":"ya existe"}}`))
		case "/garbage":
			_, _ = w.Write([]byte(`no-json`))
		}
	}))
	defer srv.Close()
	c := NewCaller("prueba", srv.URL+"/", "secreto-de-prueba", httpclient.Options{MaxAttempts: 1})

	var out struct {
		Data struct {
			Echo string `json:"echo"`
		} `json:"data"`
	}
	if err := c.Post(context.Background(), tenant, "/ok", map[string]string{"x": "hola"}, true, &out); err != nil || out.Data.Echo != "hola" {
		t.Fatalf("ok: %+v %v", out, err)
	}
	var se *StatusError
	if err := c.Post(context.Background(), tenant, "/conflict", map[string]string{}, false, nil); !errors.As(err, &se) ||
		se.Status != 409 || se.Code != "CONFLICT" || se.Message != "ya existe" {
		t.Fatalf("conflicto: %v", err)
	}
	if err := c.Post(context.Background(), tenant, "/garbage", map[string]string{}, false, &out); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatalf("respuesta ilegible: %v", err)
	}
	if err := NewCaller("vacio", "", "t", httpclient.Options{}).Post(context.Background(), tenant, "/", nil, false, nil); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatalf("sin URL: %v", err)
	}
	down := NewCaller("caido", "http://127.0.0.1:1", "t", httpclient.Options{MaxAttempts: 1, Timeout: time.Second})
	if err := down.Post(context.Background(), tenant, "/", nil, false, nil); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatalf("destino caido: %v", err)
	}
}
