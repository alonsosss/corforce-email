package transactionalcli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alonsosss/corforce-email/services/audit/internal/ports"
	"github.com/google/uuid"
)

type captured struct {
	token, tenant string
	body          map[string]any
}

func server(t *testing.T, status int, response string) (*httptest.Server, *captured) {
	t.Helper()
	got := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != sendPath {
			t.Errorf("peticion: %s %s", r.Method, r.URL.Path)
		}
		got.token, got.tenant = r.Header.Get("X-Gateway-Token"), r.Header.Get("X-Tenant-ID")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got.body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, response)
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func TestElInformeSaleComoCorreoDePlataformaEnTextoPlano(t *testing.T) {
	srv, got := server(t, http.StatusOK, `{"data":{"message_id":"`+uuid.NewString()+`","status":"queued"}}`)
	platform := uuid.New()
	suppressed, err := New(srv.URL+"/", "token-interno", platform).SendAnchorReport(context.Background(), "ops@example.org", "Asunto", "cuerpo\n")
	if err != nil || suppressed {
		t.Fatalf("%v %v", suppressed, err)
	}
	if got.token != "token-interno" || got.tenant != platform.String() {
		t.Fatalf("cabeceras: %+v", got)
	}
	if got.body["to"] != "ops@example.org" || got.body["subject"] != "Asunto" || got.body["text_body"] != "cuerpo\n" {
		t.Fatalf("cuerpo: %v", got.body)
	}
	if _, html := got.body["html_body"]; html {
		t.Fatal("el informe no lleva HTML")
	}
}

func TestUnaDireccionSuprimidaSeDistingueDeUnEnvio(t *testing.T) {
	srv, _ := server(t, http.StatusOK, `{"data":{"message_id":"`+uuid.NewString()+`","status":"suppressed"}}`)
	suppressed, err := New(srv.URL, "t", uuid.New()).SendAnchorReport(context.Background(), "ops@example.org", "A", "b")
	if err != nil || !suppressed {
		t.Fatalf("%v %v", suppressed, err)
	}
}

func TestUnRechazoNoSeConfundeConUnaCaida(t *testing.T) {
	srv, _ := server(t, http.StatusUnprocessableEntity, `{"error":{"code":"VALIDATION_ERROR","message":"PLATFORM_FROM_EMAIL no esta configurado"}}`)
	_, err := New(srv.URL, "t", uuid.New()).SendAnchorReport(context.Background(), "ops@example.org", "A", "b")
	var rejected *ports.ReportRejectedError
	if !errors.As(err, &rejected) || rejected.Status != 422 || rejected.Code != "VALIDATION_ERROR" {
		t.Fatalf("error: %v", err)
	}
	if errors.Is(err, ports.ErrReportUnavailable) {
		t.Fatal("un rechazo no es transitorio")
	}
}

func TestUnaCaidaEsTransitoria(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusTooManyRequests, http.StatusUnauthorized} {
		srv, _ := server(t, status, `{"error":{"code":"X"}}`)
		_, err := New(srv.URL, "t", uuid.New()).SendAnchorReport(context.Background(), "ops@example.org", "A", "b")
		if !errors.Is(err, ports.ErrReportUnavailable) {
			t.Fatalf("status %d: %v", status, err)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":{}}`)
	}))
	defer srv.Close()
	if _, err := New(srv.URL, "t", uuid.New()).SendAnchorReport(context.Background(), "ops@example.org", "A", "b"); !errors.Is(err, ports.ErrReportUnavailable) {
		t.Fatalf("respuesta sin estado: %v", err)
	}
	srv.Close()
	if _, err := New(srv.URL, "t", uuid.New()).SendAnchorReport(context.Background(), "ops@example.org", "A", "b"); !errors.Is(err, ports.ErrReportUnavailable) {
		t.Fatalf("sin conexion: %v", err)
	}
}
