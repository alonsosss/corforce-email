package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/zap"
)

func postAutomation(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/public/security/automation-detected", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux) HeadlessChrome/130")
	rec := httptest.NewRecorder()
	automationReportHandler(zap.NewNop()).ServeHTTP(rec, req)
	return rec
}

func TestReferenciaDeAutomatizacionValidaSeCuentaYNoDevuelveNada(t *testing.T) {
	before := testutil.ToFloat64(automationReports)
	webdriver := testutil.ToFloat64(automationSignalHits.WithLabelValues("webdriver"))
	rec := postAutomation(t, `{"reference":"k3j9d0s8a1b2","path":"/login","signals":["webdriver","zero_window"]}`)
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("esperaba 204 sin cuerpo, obtuve %d %q", rec.Code, rec.Body.String())
	}
	if testutil.ToFloat64(automationReports)-before != 1 || testutil.ToFloat64(automationSignalHits.WithLabelValues("webdriver"))-webdriver != 1 {
		t.Fatal("el bloqueo y sus senales se cuentan una vez")
	}
}

func TestReferenciaDeAutomatizacionRechazaLoQueNoEsSuyo(t *testing.T) {
	cases := map[string]string{
		"referencia con otros caracteres": `{"reference":"<script>alert(1)</script>","path":"/","signals":["webdriver"]}`,
		"referencia corta":                `{"reference":"abc","path":"/","signals":["webdriver"]}`,
		"ruta con esquema":                `{"reference":"k3j9d0s8a1b2","path":"https://evil.example/x","signals":["webdriver"]}`,
		"ruta con query":                  `{"reference":"k3j9d0s8a1b2","path":"/login?x=<b>","signals":["webdriver"]}`,
		"senal inventada":                 `{"reference":"k3j9d0s8a1b2","path":"/","signals":["cookie_stealer"]}`,
		"senal repetida":                  `{"reference":"k3j9d0s8a1b2","path":"/","signals":["webdriver","webdriver"]}`,
		"sin senales":                     `{"reference":"k3j9d0s8a1b2","path":"/","signals":[]}`,
		"campo desconocido":               `{"reference":"k3j9d0s8a1b2","path":"/","signals":["webdriver"],"extra":1}`,
		"no es json":                      `hola`,
		"cuerpo enorme":                   `{"reference":"k3j9d0s8a1b2","path":"/","signals":["webdriver"],"x":"` + strings.Repeat("a", 4096) + `"}`,
	}
	before := testutil.ToFloat64(automationReports)
	for name, body := range cases {
		if rec := postAutomation(t, body); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: esperaba 422, obtuve %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if testutil.ToFloat64(automationReports) != before {
		t.Fatal("un cuerpo rechazado no cuenta")
	}
}
