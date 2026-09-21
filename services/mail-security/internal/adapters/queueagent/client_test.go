package queueagent

import (
	"context"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
)

var claveDePrueba = strings.Repeat("q", 48)

// agente levanta un agente de prueba con TLS y devuelve un cliente que confia en su certificado (el de
// httptest, emitido para example.com).
func agente(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+claveDePrueba {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(ts.Close)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := New(Config{BaseURL: ts.URL, APIKey: claveDePrueba, ServerName: "example.com", CAFile: ca, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return c, ts
}

func TestListaPideElLimiteYLeeLaCola(t *testing.T) {
	c, _ := agente(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/queue" || r.URL.Query().Get("limit") != "25" {
			t.Errorf("%s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"total":3,"truncated":true,"items":[{"queue_id":"ABCDEF1234","queue_name":"deferred","arrival_time":1700000000,"message_size":99,"sender":"a@b.c","recipients":[{"address":"x@y.z","delay_reason":"timeout"}],"recipients_total":1}]}`))
	})
	got, err := c.List(context.Background(), 25)
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != 3 || !got.Truncated || len(got.Items) != 1 || got.Items[0].QueueID != "ABCDEF1234" || got.Items[0].Recipients[0].DelayReason != "timeout" {
		t.Fatalf("respuesta: %+v", got)
	}
}

func TestUnaColaVaciaEsUnaListaVaciaNoNil(t *testing.T) {
	c, _ := agente(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"total":0,"truncated":false,"items":null}`))
	})
	got, err := c.List(context.Background(), 10)
	if err != nil || got.Items == nil || len(got.Items) != 0 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestCadaAccionVaPorSuRuta(t *testing.T) {
	var got []string
	c, _ := agente(t, func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.Path)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	for _, a := range []domain.QueueAction{domain.QueueRetry, domain.QueueHold, domain.QueueUnhold, domain.QueueDelete} {
		if err := c.Apply(context.Background(), a, "ABCDEF1234"); err != nil {
			t.Fatalf("%s: %v", a, err)
		}
	}
	if err := c.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := "POST /v1/queue/ABCDEF1234/retry,POST /v1/queue/ABCDEF1234/hold,POST /v1/queue/ABCDEF1234/unhold,POST /v1/queue/ABCDEF1234/delete,POST /v1/queue/flush"
	if strings.Join(got, ",") != want {
		t.Fatalf("rutas:\n%s\nesperadas:\n%s", strings.Join(got, ","), want)
	}
}

func TestUnIdentificadorNoPuedeCambiarLaRuta(t *testing.T) {
	var path string
	c, _ := agente(t, func(w http.ResponseWriter, r *http.Request) { path = r.URL.EscapedPath(); _, _ = w.Write([]byte(`{}`)) })
	_ = c.Apply(context.Background(), domain.QueueDelete, "../flush")
	if strings.Contains(path, "/flush") && !strings.Contains(path, "%2F") {
		t.Fatalf("la ruta se altero: %s", path)
	}
}

func TestLosCodigosDelAgenteSeTraducenAErroresDelDominio(t *testing.T) {
	status := http.StatusOK
	c, _ := agente(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":"x"}`))
	})
	cases := []struct {
		status int
		want   error
	}{
		{http.StatusNotFound, domain.ErrNotFound},
		{http.StatusBadGateway, domain.ErrEngineUnreachable},
		{http.StatusTooManyRequests, domain.ErrEngineUnreachable},
		{http.StatusBadRequest, domain.ErrEngineCommand},
	}
	for _, tc := range cases {
		status = tc.status
		if err := c.Apply(context.Background(), domain.QueueHold, "ABCDEF1234"); !errors.Is(err, tc.want) {
			t.Errorf("HTTP %d: %v", tc.status, err)
		}
	}
}

func TestUnaClaveQueElAgenteNoAceptaEsRejected(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer ts.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	_ = os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw}), 0o600)
	c, err := New(Config{BaseURL: ts.URL, APIKey: claveDePrueba, ServerName: "example.com", CAFile: ca})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Flush(context.Background()); !errors.Is(err, domain.ErrEngineRejected) {
		t.Fatalf("401: %v", err)
	}
}

func TestUnCertificadoQueNoCasaEsRejectedYUnAgenteCaidoEsUnreachable(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	ca := filepath.Join(t.TempDir(), "ca.pem")
	_ = os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw}), 0o600)
	c, _ := New(Config{BaseURL: ts.URL, APIKey: claveDePrueba, ServerName: "otro.example", CAFile: ca})
	if err := c.Flush(context.Background()); !errors.Is(err, domain.ErrEngineRejected) {
		t.Fatalf("nombre distinto: %v", err)
	}
	ts.Close()
	if err := c.Flush(context.Background()); !errors.Is(err, domain.ErrEngineUnreachable) {
		t.Fatalf("agente caido: %v", err)
	}
}

func TestNewExigeHTTPSYUnaClaveConLaFormaDelAgente(t *testing.T) {
	ok := Config{BaseURL: "https://postfix:8590", APIKey: claveDePrueba, ServerName: "mail.example.com"}
	if _, err := New(ok); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Config){
		"http en claro":              func(c *Config) { c.BaseURL = "http://postfix:8590" },
		"clave corta":                func(c *Config) { c.APIKey = "corta" },
		"clave con espacios":         func(c *Config) { c.APIKey = strings.Repeat("a ", 20) },
		"sin nombre del certificado": func(c *Config) { c.ServerName = "" },
	} {
		cfg := ok
		mutate(&cfg)
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: debe rechazarse", name)
		}
	}
}
