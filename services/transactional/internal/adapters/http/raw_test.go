package http

import (
	"encoding/base64"
	"encoding/json"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/services/transactional/internal/app"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// El contrato de la ruta del relay se fija sin base: cada caso se rechaza antes de la primera
// consulta.
func TestInternalRawMessageContract(t *testing.T) {
	ts := newTestServer(t, "")
	h := NewHandler(Deps{UC: app.New(app.Deps{Links: ts.links, Logger: zap.NewNop()}), Perms: allowAll{}, Logger: zap.NewNop()})
	call := func(tenant string, raw string, extra string) *httptest.ResponseRecorder {
		body := `{"envelope_from":"bounces@shop.example.com","recipients":["ana@example.com"],"raw":"` +
			base64.StdEncoding.EncodeToString([]byte(raw)) + `"` + extra + `}`
		req := httptest.NewRequest(nethttp.MethodPost, "/internal/transactional/raw-messages", strings.NewReader(body))
		if tenant != "" {
			req.Header.Set("X-Tenant-ID", tenant)
		}
		rec := httptest.NewRecorder()
		h.InternalRawMessage(rec, req)
		return rec
	}
	code := func(rec *httptest.ResponseRecorder) string {
		var body errorBody
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return body.Error.Code
	}
	tenant := uuid.New().String()
	valid := "From: no-reply@shop.example.com\r\nSubject: Hola\r\n\r\nHola\r\n"

	if rec := call("", valid, ""); rec.Code != nethttp.StatusUnauthorized {
		t.Fatalf("sin empresa: %d", rec.Code)
	}
	if rec := call(tenant, "To: ana@example.com\r\n\r\nhola\r\n", ""); rec.Code != nethttp.StatusUnprocessableEntity || code(rec) != "MESSAGE_FROM_INVALID" {
		t.Fatalf("sin From: %d %s", rec.Code, rec.Body.String())
	}
	long := "From: a@shop.example.com\r\n\r\n" + strings.Repeat("x", 1200) + "\r\n"
	if rec := call(tenant, long, ""); code(rec) != "MESSAGE_LINE_TOO_LONG" {
		t.Fatalf("linea larga: %s", rec.Body.String())
	}
	broken := "From: a@shop.example.com\r\nContent-Type: multipart/mixed; boundary=b\r\n\r\n--b\r\nContent-Transfer-Encoding: base64\r\nContent-Type: application/pdf\r\n\r\n!!!\r\n--b--\r\n"
	if rec := call(tenant, broken, ""); code(rec) != "MESSAGE_MALFORMED" {
		t.Fatalf("base64 roto: %s", rec.Body.String())
	}
	if rec := call(tenant, valid, `,"campo":"desconocido"`); rec.Code != nethttp.StatusBadRequest {
		t.Fatalf("campo fuera del contrato: %d", rec.Code)
	}
	// Un sobre sin destinatarios se rechaza en el caso de uso, antes de tocar la base.
	req := httptest.NewRequest(nethttp.MethodPost, "/internal/transactional/raw-messages",
		strings.NewReader(`{"envelope_from":"a@shop.example.com","recipients":[],"raw":"`+base64.StdEncoding.EncodeToString([]byte(valid))+`"}`))
	req.Header.Set("X-Tenant-ID", tenant)
	rec := httptest.NewRecorder()
	h.InternalRawMessage(rec, req)
	if rec.Code != nethttp.StatusUnprocessableEntity {
		t.Fatalf("sin destinatarios: %d %s", rec.Code, rec.Body.String())
	}
}
