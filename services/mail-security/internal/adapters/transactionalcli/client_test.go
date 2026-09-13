package transactionalcli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/ports"
	"github.com/google/uuid"
)

type captured struct {
	token, tenant, key string
	body               map[string]any
}

func server(t *testing.T, status int, response string) (*httptest.Server, *captured) {
	t.Helper()
	got := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != messagesPath {
			t.Errorf("peticion: %s %s", r.Method, r.URL.Path)
		}
		got.token, got.tenant, got.key = r.Header.Get("X-Gateway-Token"), r.Header.Get("X-Tenant-ID"), r.Header.Get("Idempotency-Key")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got.body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, response)
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

var mail = domain.NoticeMail{From: "cuarentena@acme.com", To: "ana@acme.com", Subject: "Correo retenido", HTML: "<p>2</p>",
	IdempotencyKey: "quarantine-notice:ana@acme.com:" + uuid.NewString()}

func TestEnvioAceptadoConElContratoInterno(t *testing.T) {
	id := uuid.New()
	srv, got := server(t, http.StatusAccepted, `{"data":{"messages":[{"id":"`+id.String()+`","status":"queued"}],"suppressed":[]}}`)
	tenant := uuid.New()
	receipt, err := New(srv.URL+"/", "token-interno").SendQuarantineNotice(context.Background(), tenant, mail)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.MessageID == nil || *receipt.MessageID != id || receipt.Status != "queued" || receipt.Suppressed {
		t.Fatalf("recibo: %+v", receipt)
	}
	if got.token != "token-interno" || got.tenant != tenant.String() || got.key != mail.IdempotencyKey {
		t.Fatalf("cabeceras: %+v", got)
	}
	to, _ := got.body["to"].([]any)
	if got.body["purpose"] != "quarantine_notice" || got.body["subject"] != mail.Subject || got.body["html"] != mail.HTML || len(to) != 1 {
		t.Fatalf("cuerpo: %v", got.body)
	}
}

func TestEnvioSuprimidoYRepeticion(t *testing.T) {
	srv, _ := server(t, http.StatusOK, `{"data":{"messages":[{"id":"`+uuid.NewString()+`","status":"suppressed"}],"suppressed":[{"email":"ana@acme.com","reason":"hard_bounce"}]}}`)
	receipt, err := New(srv.URL, "t").SendQuarantineNotice(context.Background(), uuid.New(), mail)
	if err != nil || !receipt.Suppressed {
		t.Fatalf("suprimido: %+v %v", receipt, err)
	}
}

func TestClasificacionDeErrores(t *testing.T) {
	for _, tc := range []struct {
		status    int
		body      string
		transient bool
		code      string
	}{
		{422, `{"error":{"code":"SENDING_DOMAIN_NOT_VERIFIED","message":"no verificado"}}`, false, "SENDING_DOMAIN_NOT_VERIFIED"},
		{403, `{"error":{"code":"SENDING_RESTRICTED","message":"suspended"}}`, false, "SENDING_RESTRICTED"},
		{409, `{"error":{"code":"IDEMPOTENCY_KEY_REUSED"}}`, false, "IDEMPOTENCY_KEY_REUSED"},
		{400, `no es json`, false, "HTTP_400"},
		{429, `{"error":{"code":"RATE_LIMITED"}}`, true, ""},
		{401, `{"error":"unauthorized gateway"}`, true, ""},
		{503, `{"error":{"code":"SUPPRESSION_UNAVAILABLE"}}`, true, ""},
		{500, ``, true, ""},
	} {
		srv, _ := server(t, tc.status, tc.body)
		_, err := New(srv.URL, "t").SendQuarantineNotice(context.Background(), uuid.New(), mail)
		var rejected *ports.NoticeRejectedError
		switch {
		case tc.transient && !errors.Is(err, ports.ErrNoticeUnavailable):
			t.Errorf("%d: debe reintentarse, hubo %v", tc.status, err)
		case !tc.transient && (!errors.As(err, &rejected) || rejected.Code != tc.code || rejected.Status != tc.status):
			t.Errorf("%d: rechazo de negocio con su codigo, hubo %v", tc.status, err)
		}
	}
}

func TestSinTransactionalEsTransitorio(t *testing.T) {
	srv, _ := server(t, http.StatusAccepted, `{}`)
	srv.Close()
	if _, err := New(srv.URL, "t").SendQuarantineNotice(context.Background(), uuid.New(), mail); !errors.Is(err, ports.ErrNoticeUnavailable) {
		t.Fatalf("sin respuesta: %v", err)
	}
	if _, err := New("", "t").SendQuarantineNotice(context.Background(), uuid.New(), mail); !errors.Is(err, ports.ErrNoticeUnavailable) {
		t.Fatalf("sin URL: %v", err)
	}
	ok, _ := server(t, http.StatusAccepted, `{"data":{"messages":[]}}`)
	if _, err := New(ok.URL, "t").SendQuarantineNotice(context.Background(), uuid.New(), mail); !errors.Is(err, ports.ErrNoticeUnavailable) {
		t.Fatalf("respuesta sin mensajes: %v", err)
	}
}
