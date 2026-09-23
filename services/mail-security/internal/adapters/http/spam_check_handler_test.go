package http

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"
)

func rutasConPuntuacion(scanner *apptest.SpamScanner) http.Handler {
	h := NewHandler(nil, nil, nil, nil, authz.NewChecker("http://127.0.0.1:9", ""))
	if scanner != nil {
		h.WithSpamCheck(app.NewSpamCheckUseCase(scanner, nil, zap.NewNop()))
	}
	return h.Routes()
}

func puntuar(routes http.Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, SpamCheckPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	routes.ServeHTTP(rec, req)
	return rec
}

func codigo(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return body.Error.Code
}

func TestLaPuntuacionDevuelveNumerosYSimbolosOrdenados(t *testing.T) {
	scanner := &apptest.SpamScanner{Result: domain.SpamCheckResult{
		Score: decimal.RequireFromString("1.8"), Required: decimal.NewFromInt(15), Action: "no action",
		Symbols: []domain.SpamCheckSymbol{
			{Name: "MIME_HTML_ONLY", Score: decimal.RequireFromString("0.2"), Description: "Messages that have only HTML part"},
			{Name: "MISSING_MID", Score: decimal.RequireFromString("2.5"), Description: "Message id is missing"},
		},
	}}
	rec := puntuar(rutasConPuntuacion(scanner), `{"message":"From: a@acme.test\r\nSubject: Hola\r\n\r\nHola"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	want := `{"data":{"score":1.8,"required":15,"action":"no action","symbols":[` +
		`{"name":"MISSING_MID","score":2.5,"description":"Message id is missing"},` +
		`{"name":"MIME_HTML_ONLY","score":0.2,"description":"Messages that have only HTML part"}]}}`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Fatalf("respuesta:\n got %s\nwant %s", got, want)
	}
	if len(scanner.Messages) != 1 || string(scanner.Messages[0]) != "From: a@acme.test\r\nSubject: Hola\r\n\r\nHola" {
		t.Fatalf("mensaje: %q", scanner.Messages)
	}
}

func TestLaPuntuacionRechazaCuerposInvalidosAntesDeLlamarARspamd(t *testing.T) {
	scanner := &apptest.SpamScanner{Result: domain.SpamCheckResult{Action: "no action"}}
	routes := rutasConPuntuacion(scanner)
	grande := strings.Repeat("a", domain.MaxSpamCheckMessageBytes+1)
	// Cada letra escapada ocupa seis bytes del JSON: el cuerpo pasa del tope antes de decodificarse.
	enorme := strings.Repeat(`\`+"u0041", domain.MaxSpamCheckMessageBytes+1000)
	cases := []struct {
		name, body, code string
		status           int
	}{
		{"no es JSON", `{"message":`, "BAD_REQUEST", http.StatusBadRequest},
		{"campo desconocido", `{"message":"x","html":"y"}`, "BAD_REQUEST", http.StatusBadRequest},
		{"sin mensaje", `{}`, "MESSAGE_REQUIRED", http.StatusBadRequest},
		{"mensaje nulo", `{"message":null}`, "MESSAGE_REQUIRED", http.StatusBadRequest},
		{"mensaje vacio", `{"message":"  \r\n"}`, "MESSAGE_REQUIRED", http.StatusBadRequest},
		{"mensaje mayor que 2 MiB", `{"message":"` + grande + `"}`, "MESSAGE_TOO_LARGE", http.StatusRequestEntityTooLarge},
		{"cuerpo mayor que el tope", `{"message":"` + enorme + `"}`, "MESSAGE_TOO_LARGE", http.StatusRequestEntityTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := puntuar(routes, tc.body)
			if rec.Code != tc.status || codigo(t, rec) != tc.code {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
		})
	}
	if len(scanner.Messages) != 0 {
		t.Fatalf("rspamd recibio %d mensajes", len(scanner.Messages))
	}
}

func TestRspamdCaidoResponde503SpamCheckUnavailable(t *testing.T) {
	scanner := &apptest.SpamScanner{Err: fmt.Errorf("%w: conexion rechazada", domain.ErrEngineUnreachable)}
	rec := puntuar(rutasConPuntuacion(scanner), `{"message":"Subject: x\r\n\r\ny"}`)
	if rec.Code != http.StatusServiceUnavailable || codigo(t, rec) != "SPAM_CHECK_UNAVAILABLE" {
		t.Fatalf("caido: %d %s", rec.Code, rec.Body)
	}
	scanner.Err = fmt.Errorf("%w: /checkv2 ilegible", domain.ErrEngineCommand)
	if rec := puntuar(rutasConPuntuacion(scanner), `{"message":"Subject: x\r\n\r\ny"}`); rec.Code != http.StatusServiceUnavailable || codigo(t, rec) != "SPAM_CHECK_UNAVAILABLE" {
		t.Fatalf("respuesta ilegible: %d %s", rec.Code, rec.Body)
	}
	if rec := puntuar(rutasConPuntuacion(nil), `{"message":"Subject: x\r\n\r\ny"}`); rec.Code != http.StatusServiceUnavailable || codigo(t, rec) != "NOT_CONFIGURED" {
		t.Fatalf("sin caso de uso: %d %s", rec.Code, rec.Body)
	}
}

func TestSoloLaPuntuacionSinUsuarioQuedaFueraDelFiltroDeCelda(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, SpamCheckPath, nil)
	if !CellFreeRequest(req) {
		t.Fatal("la puntuacion sin usuario debe quedar fuera del filtro de celda")
	}
	for _, otra := range []*http.Request{
		httptest.NewRequest(http.MethodGet, SpamCheckPath, nil),
		httptest.NewRequest(http.MethodPost, SpamCheckPath+"/", nil),
		httptest.NewRequest(http.MethodPut, "/internal/mail-security/dkim/acme.test", nil),
	} {
		if CellFreeRequest(otra) {
			t.Fatalf("%s %s no debe saltarse el filtro de celda", otra.Method, otra.URL.Path)
		}
	}
}
