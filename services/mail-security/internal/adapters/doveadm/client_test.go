package doveadm

import (
	"context"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
)

var claveDePrueba = strings.Repeat("k", 48)

// dovecot levanta un API de doveadm de prueba con TLS y devuelve un cliente que confia en su
// certificado (el de httptest, emitido para example.com). responder decide la respuesta a cada
// cuerpo recibido.
func dovecot(t *testing.T, responder func(w http.ResponseWriter, body string)) (*Client, *httptest.Server) {
	t.Helper()
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/doveadm/v1" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "X-Dovecot-API "+base64.StdEncoding.EncodeToString([]byte(claveDePrueba)) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(r.Body)
		responder(w, string(body))
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

func fija(respuesta string) func(http.ResponseWriter, string) {
	return func(w http.ResponseWriter, _ string) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, respuesta)
	}
}

// La peticion lleva primero el vaciado de la cache y despues, si toca, el cierre de sesiones,
// con los nombres y parametros que publica GET /doveadm/v1 en Dovecot 2.3.21.1.
func TestLaPeticionVaciaYDespuesEcha(t *testing.T) {
	var recibido string
	c, _ := dovecot(t, func(w http.ResponseWriter, body string) {
		recibido = body
		_, _ = io.WriteString(w, `[["doveadmResponse",[{"entries":"1"}],"flush"],["doveadmResponse",[{"result":"ana@acme.test"}],"kick"]]`)
	})
	if err := c.ForgetCredentials(context.Background(), "ana@acme.test", true); err != nil {
		t.Fatal(err)
	}
	if want := `[["authCacheFlush",{"user":["ana@acme.test"]},"flush"],["kick",{"mask":["ana@acme.test"]},"kick"]]`; recibido != want {
		t.Fatalf("cuerpo %s, se esperaba %s", recibido, want)
	}
	if err := c.ForgetCredentials(context.Background(), "ana@acme.test", false); err != nil {
		t.Fatal(err)
	}
	if want := `[["authCacheFlush",{"user":["ana@acme.test"]},"flush"]]`; recibido != want {
		t.Fatalf("sin kick, cuerpo %s, se esperaba %s", recibido, want)
	}
}

// Respuestas capturadas de Dovecot 2.3.21.1 y su clasificacion. Un kick sin sesiones que cerrar
// (exitCode 68) es un exito: la revocacion es idempotente.
func TestClasificaLasRespuestasDeDoveadm(t *testing.T) {
	cases := []struct {
		name      string
		kick      bool
		responder func(http.ResponseWriter, string)
		want      error
	}{
		{"vaciado", false, fija(`[["doveadmResponse",[{"entries":"0"}],"flush"]]`), nil},
		{"kick sin sesiones", true, fija(`[["doveadmResponse",[{"entries":"0"}],"flush"],["error",{"type":"exitCode", "exitCode":68},"kick"]]`), nil},
		{"orden no permitida", false, fija(`[["error",{"type":"unAuthorized", "exitCode":0},"flush"]]`), domain.ErrEngineRejected},
		{"orden desconocida", true, fija(`[["doveadmResponse",[],"flush"],["error",{"type":"unknownMethod", "exitCode":0},"kick"]]`), domain.ErrEngineRejected},
		{"vaciado fallido", false, fija(`[["error",{"type":"exitCode", "exitCode":75},"flush"]]`), domain.ErrEngineCommand},
		{"vaciado sin sesiones no es exito", false, fija(`[["error",{"type":"exitCode", "exitCode":68},"flush"]]`), domain.ErrEngineCommand},
		{"sin respuesta al kick", true, fija(`[["doveadmResponse",[],"flush"]]`), domain.ErrEngineCommand},
		{"respuesta que no es JSON", false, fija(`<html>`), domain.ErrEngineCommand},
		{"forma inesperada", false, fija(`[["doveadmResponse","flush"]]`), domain.ErrEngineCommand},
		{"503", false, func(w http.ResponseWriter, _ string) { w.WriteHeader(http.StatusServiceUnavailable) }, domain.ErrEngineUnreachable},
		{"403", false, func(w http.ResponseWriter, _ string) { w.WriteHeader(http.StatusForbidden) }, domain.ErrEngineRejected},
		{"400", false, func(w http.ResponseWriter, _ string) { w.WriteHeader(http.StatusBadRequest) }, domain.ErrEngineCommand},
		{"respuesta enorme", false, fija(`[["doveadmResponse",[{"x":"` + strings.Repeat("a", maxResponseBytes) + `"}],"flush"]]`), domain.ErrEngineCommand},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := dovecot(t, tc.responder)
			err := c.ForgetCredentials(context.Background(), "ana@acme.test", tc.kick)
			if tc.want == nil {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("%v, se esperaba %v", err, tc.want)
			}
			if strings.Contains(err.Error(), claveDePrueba) {
				t.Fatal("el error lleva la clave")
			}
		})
	}
}

// Una clave distinta a la de Dovecot es un rechazo (401), sin la clave en el error; un Dovecot
// que no escucha no responde; un certificado en el que no se confia es un rechazo.
func TestClaveCertificadoYCaida(t *testing.T) {
	_, ts := dovecot(t, fija(`[]`))
	otra, err := New(Config{BaseURL: ts.URL, APIKey: strings.Repeat("x", 48), ServerName: "example.com",
		CAFile: escribirCA(t, ts), Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := otra.ForgetCredentials(context.Background(), "ana@acme.test", true); !errors.Is(err, domain.ErrEngineRejected) || strings.Contains(err.Error(), strings.Repeat("x", 48)) {
		t.Fatalf("clave distinta: %v", err)
	}

	sinCA, err := New(Config{BaseURL: ts.URL, APIKey: claveDePrueba, ServerName: "example.com", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := sinCA.ForgetCredentials(context.Background(), "ana@acme.test", true); !errors.Is(err, domain.ErrEngineRejected) {
		t.Fatalf("certificado sin CA de confianza: %v", err)
	}

	otroNombre, err := New(Config{BaseURL: ts.URL, APIKey: claveDePrueba, ServerName: "mail.otro.test", CAFile: escribirCA(t, ts), Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := otroNombre.ForgetCredentials(context.Background(), "ana@acme.test", true); !errors.Is(err, domain.ErrEngineRejected) {
		t.Fatalf("certificado de otro nombre: %v", err)
	}

	url := ts.URL
	ts.Close()
	caido, err := New(Config{BaseURL: url, APIKey: claveDePrueba, ServerName: "example.com", Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := caido.ForgetCredentials(context.Background(), "ana@acme.test", true); !errors.Is(err, domain.ErrEngineUnreachable) {
		t.Fatalf("Dovecot caido: %v", err)
	}
}

func escribirCA(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	return ca
}

// La configuracion se valida al arrancar: nunca en claro, ni con una clave que Dovecot no
// aceptaria en su configuracion, ni sin nombre de certificado. La forma de la URL (sin ruta ni
// credenciales) la valida config.ServiceURL al leer DOVEADM_API_URL (doveadm_config_test.go).
func TestNewRechazaUnaConfiguracionInsegura(t *testing.T) {
	cases := map[string]Config{
		"en claro":        {BaseURL: "http://dovecot:8443", APIKey: claveDePrueba, ServerName: "mail.acme.test"},
		"sin esquema":     {BaseURL: "dovecot:8443", APIKey: claveDePrueba, ServerName: "mail.acme.test"},
		"clave corta":     {BaseURL: "https://dovecot:8443", APIKey: "corta", ServerName: "mail.acme.test"},
		"clave con signo": {BaseURL: "https://dovecot:8443", APIKey: strings.Repeat("k", 40) + "\"x", ServerName: "mail.acme.test"},
		"sin nombre":      {BaseURL: "https://dovecot:8443", APIKey: claveDePrueba},
		"CA inexistente":  {BaseURL: "https://dovecot:8443", APIKey: claveDePrueba, ServerName: "mail.acme.test", CAFile: "/no/existe.pem"},
	}
	for name, cfg := range cases {
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: se acepto", name)
		} else if strings.Contains(err.Error(), claveDePrueba) {
			t.Errorf("%s: el error lleva la clave", name)
		}
	}
	if _, err := New(Config{BaseURL: "https://dovecot:8443", APIKey: claveDePrueba, ServerName: "mail.acme.test"}); err != nil {
		t.Fatalf("configuracion valida: %v", err)
	}
}
