package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/services/mail-auth/internal/domain"
	"github.com/google/uuid"
)

type stubVerifier struct {
	result      domain.Result
	displayName string
	got         *domain.VerifyRequest
	tenantID    uuid.UUID
	mailboxID   uuid.UUID
}

func (s *stubVerifier) Authenticate(_ context.Context, req domain.VerifyRequest) domain.Verification {
	s.got = &req
	return domain.Verification{Result: s.result, DisplayName: s.displayName, Username: "ana@empresa.pe", TenantID: s.tenantID, MailboxID: s.mailboxID}
}

func (s *stubVerifier) RecentLogins(context.Context, uuid.UUID, string, int) ([]domain.Login, error) {
	return nil, nil
}

// luaBody es, campo por campo, lo que passwd-verify.lua codifica con cjson.
const luaBody = `{"username":"ana@empresa.pe","password":"s3cr3t","real_rip":"203.0.113.7","service":"imap"}`

func post(t *testing.T, h http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeSuccess(t *testing.T, rec *httptest.ResponseRecorder) bool {
	t.Helper()
	var body struct {
		Success *bool `json:"success"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("cuerpo no es JSON: %q", rec.Body.String())
	}
	if body.Success == nil {
		t.Fatalf("el cuerpo debe llevar success en la raiz: %q", rec.Body.String())
	}
	return *body.Success
}

func TestVerifyDevuelve200ConSuccessTrue(t *testing.T) {
	stub := &stubVerifier{result: domain.ResultOK}
	h := NewHandler(stub).VerifyRoutes()

	for _, path := range []string{"/", "/auth"} {
		rec := post(t, h, path, luaBody)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d", path, rec.Code)
		}
		if !decodeSuccess(t, rec) {
			t.Fatalf("%s: success debe ser true", path)
		}
	}
	if stub.got == nil || stub.got.Username != "ana@empresa.pe" || stub.got.Password != "s3cr3t" ||
		stub.got.RemoteIP != "203.0.113.7" || stub.got.Service != "imap" {
		t.Fatalf("la peticion no llego integra al caso de uso: %+v", stub.got)
	}
}

func TestVerifyDevuelve401ConSuccessFalse(t *testing.T) {
	for _, result := range []domain.Result{
		domain.ResultBadPassword, domain.ResultInactive, domain.ResultNoAccess,
		domain.ResultThrottled, domain.ResultUnknownService, domain.ResultError,
	} {
		h := NewHandler(&stubVerifier{result: result}).VerifyRoutes()
		rec := post(t, h, "/", luaBody)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status = %d, se esperaba 401", result, rec.Code)
		}
		if decodeSuccess(t, rec) {
			t.Fatalf("%s: success debe ser false", result)
		}
	}
}

func TestVerifyDevuelve400SiFaltanCampos(t *testing.T) {
	stub := &stubVerifier{result: domain.ResultOK}
	h := NewHandler(stub).VerifyRoutes()

	cases := map[string]string{
		"sin username": `{"password":"x","real_rip":"203.0.113.7","service":"imap"}`,
		"sin password": `{"username":"ana@empresa.pe","real_rip":"203.0.113.7","service":"imap"}`,
		"sin real_rip": `{"username":"ana@empresa.pe","password":"x","service":"imap"}`,
		"json roto":    `{"username":`,
		"vacio":        ``,
	}
	for name, body := range cases {
		rec := post(t, h, "/", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, se esperaba 400", name, rec.Code)
		}
		if decodeSuccess(t, rec) {
			t.Fatalf("%s: success debe ser false", name)
		}
	}
	if stub.got != nil {
		t.Fatal("un cuerpo incompleto no debe llegar al caso de uso")
	}
}

func TestVerifySinServicioLlegaAlCasoDeUso(t *testing.T) {
	// service no es obligatorio en el contrato: su ausencia la decide el caso de uso
	// (servicio desconocido -> 401), no el adaptador.
	stub := &stubVerifier{result: domain.ResultUnknownService}
	h := NewHandler(stub).VerifyRoutes()
	rec := post(t, h, "/", `{"username":"ana@empresa.pe","password":"x","real_rip":"203.0.113.7"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, se esperaba 401", rec.Code)
	}
	if stub.got == nil || stub.got.Service != "" {
		t.Fatalf("peticion inesperada: %+v", stub.got)
	}
}

func TestVerifyDevuelveElNombreSoloAlWebmail(t *testing.T) {
	stub := &stubVerifier{result: domain.ResultOK, displayName: "Ana Perez"}
	h := NewHandler(stub).VerifyRoutes()

	decode := func(rec *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("cuerpo no es JSON: %q", rec.Body.String())
		}
		return body
	}

	webmail := post(t, h, "/", `{"username":"ana@empresa.pe","password":"s3cr3t","real_rip":"203.0.113.7","service":"webmail"}`)
	if webmail.Code != http.StatusOK {
		t.Fatalf("webmail: status = %d", webmail.Code)
	}
	if got := decode(webmail)["display_name"]; got != "Ana Perez" {
		t.Fatalf("webmail: display_name = %v", got)
	}

	// Dovecot sigue recibiendo exactamente {"success":true}.
	imap := post(t, h, "/", luaBody)
	body := decode(imap)
	if _, ok := body["display_name"]; ok || len(body) != 1 {
		t.Fatalf("imap: el cuerpo debe llevar solo success: %q", imap.Body.String())
	}

	// Un rechazo nunca revela el nombre.
	stub.result = domain.ResultBadPassword
	denied := post(t, h, "/", `{"username":"ana@empresa.pe","password":"x","real_rip":"203.0.113.7","service":"webmail"}`)
	if denied.Code != http.StatusUnauthorized {
		t.Fatalf("rechazo: status = %d", denied.Code)
	}
	if _, ok := decode(denied)["display_name"]; ok {
		t.Fatalf("rechazo: no debe llevar display_name: %q", denied.Body.String())
	}
}

func TestVerifyRechazaOtrosMetodos(t *testing.T) {
	h := NewHandler(&stubVerifier{result: domain.ResultOK}).VerifyRoutes()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, se esperaba 405", rec.Code)
	}
}

// mail-dav no puede deducir la empresa ni el buzon del nombre: solo a el se le devuelven, y solo si entra.
func TestVerifyDevuelveLaIdentidadSoloAMailDAV(t *testing.T) {
	stub := &stubVerifier{result: domain.ResultOK, displayName: "Ana Perez", tenantID: uuid.New(), mailboxID: uuid.New()}
	h := NewHandler(stub).VerifyRoutes()
	decode := func(rec *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("cuerpo no es JSON: %q", rec.Body.String())
		}
		return body
	}

	dav := decode(post(t, h, "/", `{"username":"Ana@Empresa.PE","password":"s3cr3t","real_rip":"203.0.113.7","service":"dav"}`))
	if dav["success"] != true || dav["username"] != "ana@empresa.pe" || dav["tenant_id"] != stub.tenantID.String() ||
		dav["mailbox_id"] != stub.mailboxID.String() {
		t.Fatalf("dav: %v", dav)
	}
	if _, ok := dav["display_name"]; ok {
		t.Fatalf("dav no necesita el nombre visible: %v", dav)
	}
	for _, service := range []string{"imap", "webmail", "sieve"} {
		body := decode(post(t, h, "/", `{"username":"ana@empresa.pe","password":"s3cr3t","real_rip":"203.0.113.7","service":"`+service+`"}`))
		for _, k := range []string{"username", "tenant_id", "mailbox_id"} {
			if _, ok := body[k]; ok {
				t.Fatalf("%s no debe recibir %s: %v", service, k, body)
			}
		}
	}

	stub.result = domain.ResultNoAccess
	rec := post(t, h, "/", `{"username":"ana@empresa.pe","password":"x","real_rip":"203.0.113.7","service":"dav"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("rechazo: status = %d", rec.Code)
	}
	for _, k := range []string{"username", "tenant_id", "mailbox_id"} {
		if _, ok := decode(rec)[k]; ok {
			t.Fatalf("un rechazo no debe llevar %s: %q", k, rec.Body.String())
		}
	}
}
