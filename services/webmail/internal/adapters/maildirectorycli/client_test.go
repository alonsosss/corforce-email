package maildirectorycli

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

func TestSenderIdentitiesLlamadaInterna(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != identitiesPath {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("X-Gateway-Token") != "token-interno" || r.Header.Get("X-Tenant-ID") != "" {
			t.Errorf("cabeceras: %v", r.Header)
		}
		if r.URL.Query().Get("username") != "ana+x@empresa.pe" {
			t.Errorf("username: %q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"addresses":["Ventas@Empresa.PE","ñ@empresa.pe","ana+x@empresa.pe","a@b@c"]}}`))
	}))
	defer srv.Close()

	c, err := New(srv.URL+"/", "token-interno")
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.SenderIdentities(context.Background(), "ana+x@empresa.pe")
	if err != nil {
		t.Fatal(err)
	}
	// Las que el webmail no podria poner en el sobre SMTP no se ofrecen.
	if strings.Join(got, ",") != "ventas@empresa.pe,ana+x@empresa.pe" {
		t.Fatalf("got %v", got)
	}
}

func TestSenderIdentitiesFallos(t *testing.T) {
	var calls atomic.Int32
	status, body := http.StatusServiceUnavailable, ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	c, err := New(srv.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.SenderIdentities(context.Background(), "ana@empresa.pe"); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("503: %v", err)
	}
	if calls.Load() < 2 {
		t.Fatalf("una lectura se reintenta ante un 503: %d", calls.Load())
	}
	status, body = http.StatusOK, "<html>"
	if _, err := c.SenderIdentities(context.Background(), "ana@empresa.pe"); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("respuesta ilegible: %v", err)
	}
}

func TestNewExigeURLValida(t *testing.T) {
	for _, bad := range []string{"", "mail-directory:8040", "ftp://mail-directory", "http://usuario:clave@mail-directory:8040"} {
		if _, err := New(bad, "x"); err == nil {
			t.Errorf("%q: se esperaba error", bad)
		}
	}
}
