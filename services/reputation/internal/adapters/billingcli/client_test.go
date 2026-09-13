package billingcli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alonsosss/corforce-email/services/reputation/internal/domain"
	"github.com/google/uuid"
)

func TestCheckConsultaConElContratoDeBilling(t *testing.T) {
	tenant := uuid.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != checkPath {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("X-Gateway-Token") != "token-interno" || r.Header.Get("X-Tenant-ID") != tenant.String() {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body checkRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Resource != "marketing_messages" || body.Quantity != 25 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"allowed":true,"resource":"marketing_messages","limit":1000,"used":400,"remaining":600,"hard_limit":true}}`))
	}))
	defer srv.Close()

	ent, err := New(srv.URL+"/", "token-interno").Check(context.Background(), tenant, domain.ClassMarketing, 25)
	if err != nil {
		t.Fatal(err)
	}
	if !ent.Allowed || !ent.HardLimit || *ent.Limit != 1000 || ent.Used != 400 || *ent.Remaining != 600 || ent.Requested != 25 {
		t.Fatalf("derecho: %+v", ent)
	}
}

func TestCheckSinLimiteYDenegado(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"allowed":false,"resource":"transactional_messages","limit":null,"used":7,"remaining":null,"hard_limit":false,"reason":"no_subscription"}}`))
	}))
	defer srv.Close()

	ent, err := New(srv.URL, "t").Check(context.Background(), uuid.New(), domain.ClassTransactional, 1)
	if err != nil {
		t.Fatal(err)
	}
	if ent.Allowed || ent.Limit != nil || ent.Remaining != nil || ent.Reason != "no_subscription" {
		t.Fatalf("derecho: %+v", ent)
	}
}

func TestCheckErrores(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"status no 200": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) },
		"cuerpo ilegible": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"data":`))
		},
		"otro recurso": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"data":{"allowed":true,"resource":"contacts"}}`))
		},
	}
	for name, h := range cases {
		srv := httptest.NewServer(h)
		_, err := New(srv.URL, "t").Check(context.Background(), uuid.New(), domain.ClassMarketing, 1)
		srv.Close()
		if err == nil {
			t.Errorf("%s: se esperaba error", name)
		}
	}
}
