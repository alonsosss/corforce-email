package http

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	nethttp "net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/transactional/internal/adapters/sns"
	"github.com/alonsosss/corforce-email/services/transactional/internal/app"
	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const certURL = "https://sns.us-east-1.amazonaws.com/SimpleNotificationService-test.pem"

type allowAll struct{}

func (allowAll) RequirePermission(string, string, string) func(nethttp.Handler) nethttp.Handler {
	return func(next nethttp.Handler) nethttp.Handler { return next }
}

type testServer struct {
	handler nethttp.Handler
	key     *rsa.PrivateKey
	links   *domain.LinkSigner
	fetched []string
}

// newTestServer arma el handler sin base de datos: las rutas que se prueban aqui
// deciden antes de resolver la empresa (firma, enlace, coherencia de empresa).
func newTestServer(t *testing.T, topicARN string) *testServer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "sns"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	links, err := domain.NewLinkSigner("0123456789abcdef0123456789abcdef-handler", "https://app.example.com")
	if err != nil {
		t.Fatal(err)
	}
	ts := &testServer{key: key, links: links}
	verifier := sns.NewVerifierWithFetcher(func(_ context.Context, u string) ([]byte, error) {
		ts.fetched = append(ts.fetched, u)
		if u == certURL {
			return certPEM, nil
		}
		return nil, errors.New("no servido")
	})
	uc := app.New(app.Deps{Links: links, Logger: zap.NewNop()})
	ts.handler = NewHandler(Deps{UC: uc, Perms: allowAll{}, SNS: verifier, TopicARN: topicARN, Logger: zap.NewNop()}).Routes()
	return ts
}

// signEnvelope firma con la cadena canonica documentada por AWS, escrita aqui de forma
// independiente de la del verificador para contrastarla.
func (ts *testServer) signEnvelope(t *testing.T, e *sns.Envelope) {
	t.Helper()
	var b strings.Builder
	add := func(k, v string) { b.WriteString(k + "\n" + v + "\n") }
	if e.Type == sns.TypeNotification {
		add("Message", e.Message)
		add("MessageId", e.MessageID)
		if e.Subject != "" {
			add("Subject", e.Subject)
		}
		add("Timestamp", e.Timestamp)
		add("TopicArn", e.TopicArn)
		add("Type", e.Type)
	} else {
		add("Message", e.Message)
		add("MessageId", e.MessageID)
		add("SubscribeURL", e.SubscribeURL)
		add("Timestamp", e.Timestamp)
		add("Token", e.Token)
		add("TopicArn", e.TopicArn)
		add("Type", e.Type)
	}
	e.SignatureVersion = "2"
	e.SigningCertURL = certURL
	h := crypto.SHA256.New()
	h.Write([]byte(b.String()))
	sig, err := rsa.SignPKCS1v15(rand.Reader, ts.key, crypto.SHA256, h.Sum(nil))
	if err != nil {
		t.Fatal(err)
	}
	e.Signature = base64.StdEncoding.EncodeToString(sig)
}

func (ts *testServer) do(method, target, contentType string, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	ts.handler.ServeHTTP(rec, req)
	return rec
}

func sesNotification(tenant uuid.UUID) *sns.Envelope {
	msg := `{"eventType":"Bounce","mail":{"messageId":"0100-x","destination":["ana@example.com"],` +
		`"tags":{"tenant_id":["` + tenant.String() + `"],"message_id":["` + uuid.New().String() + `"]}},` +
		`"bounce":{"bounceType":"Permanent","bouncedRecipients":[{"emailAddress":"ana@example.com"}]}}`
	return &sns.Envelope{
		Type: sns.TypeNotification, MessageID: uuid.New().String(),
		TopicArn:  "arn:aws:sns:us-east-1:123456789012:cfm-transactional-events",
		Message:   msg,
		Timestamp: "2026-09-12T12:00:00.000Z",
	}
}

func marshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSESEventsRejectsInvalidSignature(t *testing.T) {
	ts := newTestServer(t, "")
	tenant := uuid.New()
	e := sesNotification(tenant)
	ts.signEnvelope(t, e)
	e.Message = strings.Replace(e.Message, "Permanent", "Transient", 1)
	rec := ts.do(nethttp.MethodPost, "/api/v1/public/transactional/ses-events/"+tenant.String(), "text/plain", marshal(t, e))
	if rec.Code != nethttp.StatusForbidden {
		t.Fatalf("firma alterada: status %d", rec.Code)
	}
}

func TestSESEventsRejectsTenantMismatch(t *testing.T) {
	ts := newTestServer(t, "")
	e := sesNotification(uuid.New())
	ts.signEnvelope(t, e)
	rec := ts.do(nethttp.MethodPost, "/api/v1/public/transactional/ses-events/"+uuid.New().String(), "text/plain", marshal(t, e))
	if rec.Code != nethttp.StatusForbidden {
		t.Fatalf("un evento de otra empresa: status %d", rec.Code)
	}
}

func TestSESEventsRejectsForeignTopic(t *testing.T) {
	ts := newTestServer(t, "arn:aws:sns:us-east-1:123456789012:esperado")
	tenant := uuid.New()
	e := sesNotification(tenant)
	ts.signEnvelope(t, e)
	rec := ts.do(nethttp.MethodPost, "/api/v1/public/transactional/ses-events/"+tenant.String(), "text/plain", marshal(t, e))
	if rec.Code != nethttp.StatusForbidden {
		t.Fatalf("topic no esperado: status %d", rec.Code)
	}
}

func TestSESEventsBodyLimit(t *testing.T) {
	ts := newTestServer(t, "")
	body := `{"Type":"Notification","Message":"` + strings.Repeat("a", maxSNSBody) + `"}`
	rec := ts.do(nethttp.MethodPost, "/api/v1/public/transactional/ses-events/"+uuid.New().String(), "text/plain", body)
	if rec.Code != nethttp.StatusBadRequest {
		t.Fatalf("cuerpo por encima de 1 MiB: status %d", rec.Code)
	}
	if len(ts.fetched) != 0 {
		t.Fatal("no se descarga ningun certificado para un cuerpo rechazado")
	}
}

func TestSESEventsSubscriptionConfirmationGuardsSSRF(t *testing.T) {
	ts := newTestServer(t, "")
	e := &sns.Envelope{
		Type: sns.TypeSubscriptionConfirmation, MessageID: uuid.New().String(), Token: "tok",
		TopicArn: "arn:aws:sns:us-east-1:123456789012:cfm", Message: "confirm",
		SubscribeURL: "https://169.254.169.254/latest/meta-data/iam/", Timestamp: "2026-09-12T12:00:00.000Z",
	}
	ts.signEnvelope(t, e)
	rec := ts.do(nethttp.MethodPost, "/api/v1/public/transactional/ses-events/"+uuid.New().String(), "text/plain", marshal(t, e))
	if rec.Code != nethttp.StatusForbidden {
		t.Fatalf("SubscribeURL interna: status %d", rec.Code)
	}
	for _, u := range ts.fetched {
		if u != certURL {
			t.Fatalf("no debe visitarse %q", u)
		}
	}
}

func unsubscribeURL(ts *testServer, email string) (string, domain.UnsubscribeClaims) {
	claims := domain.UnsubscribeClaims{TenantID: uuid.New(), MessageID: uuid.New(), Email: email}
	full := ts.links.UnsubscribeURL(claims)
	u, _ := url.Parse(full)
	return u.RequestURI(), claims
}

func TestUnsubscribePageValidLink(t *testing.T) {
	ts := newTestServer(t, "")
	target, _ := unsubscribeURL(ts, "ana@example.com")
	rec := ts.do(nethttp.MethodGet, target, "", "")
	body := rec.Body.String()
	if rec.Code != nethttp.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("pagina de confirmacion: status %d", rec.Code)
	}
	if !strings.Contains(body, `method="post"`) || !strings.Contains(body, "ana@example.com") {
		t.Fatalf("la pagina debe ofrecer el boton de baja: %s", body)
	}
	if strings.Contains(strings.ToLower(body), "<script") {
		t.Fatal("la pagina no puede llevar JavaScript (CSP del gateway)")
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("la pagina no se cachea")
	}
}

func TestUnsubscribeRejectsTamperedLink(t *testing.T) {
	ts := newTestServer(t, "")
	target, _ := unsubscribeURL(ts, "ana@example.com")
	tampered := strings.Replace(target, "ana%40example.com", "eva%40example.com", 1)
	if tampered == target {
		t.Fatal("la prueba no altero el enlace")
	}
	if rec := ts.do(nethttp.MethodGet, tampered, "", ""); rec.Code != nethttp.StatusForbidden {
		t.Fatalf("GET con enlace alterado: status %d", rec.Code)
	}
	rec := ts.do(nethttp.MethodPost, tampered, "application/x-www-form-urlencoded", "List-Unsubscribe=One-Click")
	if rec.Code != nethttp.StatusForbidden {
		t.Fatalf("POST One-Click con enlace alterado: status %d", rec.Code)
	}
	if rec := ts.do(nethttp.MethodGet, "/api/v1/public/transactional/unsubscribe", "", ""); rec.Code != nethttp.StatusForbidden {
		t.Fatalf("sin parametros: status %d", rec.Code)
	}
}

// La ruta sin empresa confia en la etiqueta tenant_id del evento firmado; sin un topic
// fijado, cualquier topic con firma valida de SNS podria atribuirse eventos de otra
// empresa. Se rechaza antes de tocar la red: ni siquiera se descarga el certificado.
func TestSESEventsSinEmpresaExigeTopicConfigurado(t *testing.T) {
	ts := newTestServer(t, "")
	e := sesNotification(uuid.New())
	ts.signEnvelope(t, e)
	rec := ts.do(nethttp.MethodPost, "/api/v1/public/transactional/ses-events", "text/plain", marshal(t, e))
	if rec.Code != nethttp.StatusForbidden {
		t.Fatalf("sin topic configurado: status %d", rec.Code)
	}
	if len(ts.fetched) != 0 {
		t.Fatalf("no debe descargar certificados sin topic configurado: %v", ts.fetched)
	}
}

func TestSESEventsSinEmpresaRechazaTopicAjeno(t *testing.T) {
	ts := newTestServer(t, "arn:aws:sns:us-east-1:123456789012:esperado")
	e := sesNotification(uuid.New())
	ts.signEnvelope(t, e)
	rec := ts.do(nethttp.MethodPost, "/api/v1/public/transactional/ses-events", "text/plain", marshal(t, e))
	if rec.Code != nethttp.StatusForbidden {
		t.Fatalf("topic ajeno en la ruta sin empresa: status %d", rec.Code)
	}
}

type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func TestWriteErrorTranslatesSendingDecisions(t *testing.T) {
	wait, noWait := 1500*time.Millisecond, time.Duration(0)
	cases := []struct {
		name       string
		err        error
		status     int
		code       string
		message    string // prefijo esperado del mensaje
		retryAfter string
	}{
		{"tasa con espera", &domain.SendingDeniedError{Class: "marketing", Reason: "rate_limited", RetryAfter: &wait}, 429, "RATE_LIMITED", "rate_limited", "2"},
		{"tasa con espera cero", &domain.SendingDeniedError{Reason: "rate_limited", RetryAfter: &noWait}, 429, "RATE_LIMITED", "rate_limited", "1"},
		{"lote que no cabe en la ventana", &domain.SendingDeniedError{Reason: "rate_limited"}, 403, "SENDING_RESTRICTED", "rate_limited: lo pedido supera", ""},
		{"suspendida", &domain.SendingDeniedError{Reason: "suspended"}, 403, "SENDING_RESTRICTED", "suspended", ""},
		{"reputacion", fmt.Errorf("lote: %w", &domain.SendingDeniedError{Reason: "reputation_restricted"}), 403, "SENDING_RESTRICTED", "reputation_restricted", ""},
		{"plan segun billing", &domain.SendingDeniedError{Reason: "limit_reached"}, 403, "PLAN_LIMIT_REACHED", "limit_reached", ""},
		{"sin suscripcion", &domain.SendingDeniedError{Reason: "no_subscription"}, 403, "PLAN_LIMIT_REACHED", "no_subscription", ""},
		{"plan por defecto", &domain.SendingDeniedError{Reason: "plan_limit_exceeded"}, 403, "PLAN_LIMIT_REACHED", "plan_limit_exceeded", ""},
		{"motivo vacio", &domain.SendingDeniedError{}, 403, "PLAN_LIMIT_REACHED", "plan_denied", ""},
		{"reputation caido", domain.ErrReputationUnavailable, 503, "REPUTATION_UNAVAILABLE", "", ""},
		{"suppression caido", domain.ErrSuppressionUnavailable, 503, "SUPPRESSION_UNAVAILABLE", "", ""},
		{"plantilla no marketing", domain.ErrTemplateNotMarketing, 422, "TEMPLATE_NOT_MARKETING", "", ""},
		{"clave de otra clase", domain.ErrIdempotencyKeyReused, 409, "IDEMPOTENCY_KEY_REUSED", "", ""},
		{"dominio sin verificar", domain.ErrSendingDomainNotVerified, 422, "SENDING_DOMAIN_NOT_VERIFIED", "", ""},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		writeError(rec, tc.err)
		var body errorBody
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if rec.Code != tc.status || body.Error.Code != tc.code {
			t.Errorf("%s: %d %s, se esperaba %d %s", tc.name, rec.Code, body.Error.Code, tc.status, tc.code)
		}
		if tc.message != "" && !strings.HasPrefix(body.Error.Message, tc.message) {
			t.Errorf("%s: message = %q", tc.name, body.Error.Message)
		}
		if got := rec.Header().Get("Retry-After"); got != tc.retryAfter {
			t.Errorf("%s: Retry-After = %q, se esperaba %q", tc.name, got, tc.retryAfter)
		}
	}
}

// El lote se prueba sin base: una peticion que solo falla en la ultima validacion
// (las etiquetas) demuestra que cada campo del contrato se decodifica y llega al caso de
// uso; si alguno se perdiera, el error seria el de ese campo.
func TestMarketingBatchDecodesContract(t *testing.T) {
	ts := newTestServer(t, "")
	h := NewHandler(Deps{UC: app.New(app.Deps{Links: ts.links, Logger: zap.NewNop()}), Perms: allowAll{}, Logger: zap.NewNop()})
	call := func(tenant, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(nethttp.MethodPost, "/internal/transactional/batch", strings.NewReader(body))
		if tenant != "" {
			req.Header.Set("X-Tenant-ID", tenant)
		}
		rec := httptest.NewRecorder()
		h.MarketingBatch(rec, req)
		return rec
	}
	valid := `{"class":"marketing","campaign_id":"` + uuid.New().String() + `","idempotency_key":"c-1-b-1",` +
		`"from":{"email":"news@shop.example.com","name":"Tienda"},"reply_to":"soporte@shop.example.com",` +
		`"template_id":"` + uuid.New().String() + `","template_version":3,` +
		`"recipients":[{"email":"ana@example.com","name":"Ana","contact_id":"` + uuid.New().String() + `","variables":{"first_name":"Ana"}}],` +
		`"tags":{"message_id":"reservada"}}`

	if rec := call("", valid); rec.Code != nethttp.StatusUnauthorized {
		t.Fatalf("sin X-Tenant-ID: %d", rec.Code)
	}
	tenant := uuid.New().String()
	rec := call(tenant, valid)
	var body errorBody
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != nethttp.StatusUnprocessableEntity || !strings.Contains(body.Error.Message, "tags") {
		t.Fatalf("todos los campos del contrato deben llegar al caso de uso: %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(tenant, strings.Replace(valid, `"class":"marketing"`, `"class":"transactional"`, 1)); rec.Code != nethttp.StatusUnprocessableEntity {
		t.Fatalf("la ruta solo admite marketing: %d", rec.Code)
	}
	if rec := call(tenant, strings.Replace(valid, `"reply_to"`, `"reply"`, 1)); rec.Code != nethttp.StatusBadRequest {
		t.Fatalf("un campo fuera del contrato se rechaza: %d", rec.Code)
	}
}

func TestSESEventsSinEmpresaRechazaFirmaAlterada(t *testing.T) {
	ts := newTestServer(t, "arn:aws:sns:us-east-1:123456789012:cfm-transactional-events")
	e := sesNotification(uuid.New())
	ts.signEnvelope(t, e)
	e.Message = strings.Replace(e.Message, "Permanent", "Transient", 1)
	rec := ts.do(nethttp.MethodPost, "/api/v1/public/transactional/ses-events", "text/plain", marshal(t, e))
	if rec.Code != nethttp.StatusForbidden {
		t.Fatalf("firma alterada en la ruta sin empresa: status %d", rec.Code)
	}
}
