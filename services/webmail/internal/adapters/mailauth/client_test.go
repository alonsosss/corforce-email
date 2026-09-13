package mailauth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

func tlsServer(t *testing.T, h http.HandlerFunc) (*httptest.Server, *tls.Config) {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return srv, &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
}

func newClient(t *testing.T, url string, cfg *tls.Config) *Client {
	t.Helper()
	c, err := New(url, cfg, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestVerifyEnviaElContratoDeDovecotConServicioWebmail(t *testing.T) {
	var got map[string]string
	srv, cfg := tlsServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"display_name":"Ana Pérez"}`))
	})
	id, err := newClient(t, srv.URL, cfg).Verify(context.Background(), "ana@empresa.pe", "s3cr3t", "203.0.113.7")
	if err != nil {
		t.Fatal(err)
	}
	if id.Username != "ana@empresa.pe" || id.DisplayName != "Ana Pérez" {
		t.Fatalf("identidad: %+v", id)
	}
	want := map[string]string{"username": "ana@empresa.pe", "password": "s3cr3t", "real_rip": "203.0.113.7", "service": "webmail"}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("campo %s = %q, se esperaba %q", k, got[k], v)
		}
	}
}

func TestVerifyRechazosSonCredencialesInvalidas(t *testing.T) {
	for _, c := range []struct {
		status int
		body   string
	}{
		{http.StatusUnauthorized, `{"success":false}`},
		{http.StatusOK, `{"success":false}`},
	} {
		srv, cfg := tlsServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(c.status)
			_, _ = w.Write([]byte(c.body))
		})
		if _, err := newClient(t, srv.URL, cfg).Verify(context.Background(), "ana@empresa.pe", "x", "203.0.113.7"); err != domain.ErrInvalidCredentials {
			t.Fatalf("%d %s: %v", c.status, c.body, err)
		}
	}
}

func TestVerifyFallosSonNoDisponible(t *testing.T) {
	redirected := false
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirected = true
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer target.Close()

	for name, h := range map[string]http.HandlerFunc{
		"500":          func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
		"json roto":    func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"success":`)) },
		"redireccion":  func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect) },
		"cuerpo vacio": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadRequest) },
	} {
		srv, cfg := tlsServer(t, h)
		if _, err := newClient(t, srv.URL, cfg).Verify(context.Background(), "ana@empresa.pe", "x", "203.0.113.7"); !errors.Is(err, domain.ErrUnavailable) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if redirected {
		t.Fatal("una redireccion no se sigue: reenviaria la contrasena")
	}
}

func TestVerifyExigeCertificadoVerificado(t *testing.T) {
	srv, _ := tlsServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"success":true}`))
	})
	c := newClient(t, srv.URL, &tls.Config{MinVersion: tls.VersionTLS12})
	if _, err := c.Verify(context.Background(), "ana@empresa.pe", "x", "203.0.113.7"); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("un certificado no confiable no recibe la contrasena: %v", err)
	}
}

func TestVerifyDescartaNombreConSaltosDeLinea(t *testing.T) {
	srv, cfg := tlsServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"display_name":"Ana\r\nBcc: espia@x.com"}`))
	})
	id, err := newClient(t, srv.URL, cfg).Verify(context.Background(), "ana@empresa.pe", "x", "203.0.113.7")
	if err != nil || id.DisplayName != "" {
		t.Fatalf("un nombre que no cabe en una cabecera se descarta: %+v %v", id, err)
	}
}

func TestNewExigeHTTPS(t *testing.T) {
	for _, u := range []string{"http://mail-auth:9082", "mail-auth:9082", "https://", ""} {
		if _, err := New(u, &tls.Config{}, time.Second); err == nil {
			t.Fatalf("%q debe rechazarse", u)
		}
	}
}
