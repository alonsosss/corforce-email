package transactionalcli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/smtp-relay/internal/domain"
)

func TestSubmit(t *testing.T) {
	var got request
	var headers http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers = r.Header.Clone()
		if r.URL.Path != rawPath || r.Method != http.MethodPost {
			t.Errorf("ruta: %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"data":{"messages":[{"id":"m-1","status":"queued"}],"suppressed":[]}}`))
	}))
	defer srv.Close()
	c := New(srv.URL+"/", "token-interno", 5*time.Second)
	id, err := c.Submit(context.Background(), domain.Credential{KeyID: "k1", TenantID: "t1"},
		domain.Envelope{From: "a@empresa.test", Recipients: []string{"b@x.test"}}, []byte("MIME"), "smtp-1")
	if err != nil || id != "m-1" {
		t.Fatalf("aceptado: %q %v", id, err)
	}
	if headers.Get("X-Tenant-ID") != "t1" || headers.Get("X-Gateway-Token") != "token-interno" {
		t.Fatalf("cabeceras: %v", headers)
	}
	if string(got.Raw) != "MIME" || got.APIKeyID != "k1" || got.IdempotencyKey != "smtp-1" || got.Recipients[0] != "b@x.test" {
		t.Fatalf("cuerpo: %+v", got)
	}
}

func TestSubmitRespuestasDeError(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   *domain.Rejection
	}{
		{422, `{"error":{"code":"SENDING_DOMAIN_NOT_VERIFIED"}}`, domain.ErrSenderNotVerified},
		{422, `{"error":{"code":"MESSAGE_MALFORMED"}}`, domain.ErrMalformed},
		{413, `{"error":{"code":"MESSAGE_TOO_LARGE"}}`, domain.ErrTooLarge},
		{429, `{"error":{"code":"RATE_LIMITED"}}`, domain.ErrSendingThrottled},
		{403, `{"error":{"code":"PLAN_LIMIT_REACHED"}}`, domain.ErrSendingDenied},
		{403, `{"error":{"code":"SENDING_RESTRICTED"}}`, domain.ErrSendingDenied},
		{422, `{"error":{"code":"VALIDATION_ERROR"}}`, domain.ErrRejected},
		{503, `{"error":{"code":"SUPPRESSION_UNAVAILABLE"}}`, domain.ErrUpstream},
		{502, `no es json`, domain.ErrUpstream},
		{202, `{"data":{"messages":[]}}`, domain.ErrUpstream},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		_, err := New(srv.URL, "", time.Second).Submit(context.Background(), domain.Credential{TenantID: "t1"},
			domain.Envelope{From: "a@b.test", Recipients: []string{"c@d.test"}}, []byte("x"), "")
		srv.Close()
		if !errors.Is(err, tc.want) {
			t.Errorf("%d %s: %v, se esperaba %s", tc.status, tc.body, err, tc.want.Reason)
		}
	}
	if _, err := New("http://127.0.0.1:1", "", time.Second).Submit(context.Background(), domain.Credential{}, domain.Envelope{}, nil, ""); !errors.Is(err, domain.ErrUpstream) {
		t.Errorf("transactional inalcanzable: %v", err)
	}
}
