package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/middleware"
)

func setPassEnv(t *testing.T, until, cidrs string) {
	t.Helper()
	t.Setenv("WEB_AUTOMATION_OBSERVE_UNTIL", until)
	t.Setenv("WEB_AUTOMATION_OBSERVE_CIDRS", cidrs)
}

func TestPaseDeAutomatizacionSinVariableNoExiste(t *testing.T) {
	setPassEnv(t, "", "203.0.113.0/24")
	pass, err := loadAutomationPass(time.Now)
	if err != nil || pass != nil {
		t.Fatalf("sin WEB_AUTOMATION_OBSERVE_UNTIL no hay pase: %v %v", pass, err)
	}
	if pass.open() || pass.allows("203.0.113.7") {
		t.Fatal("un pase inexistente no abre nada")
	}
}

func TestPaseDeAutomatizacionRechazaLoMalEscrito(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	cases := map[string][2]string{
		"fecha sin formato":        {"manana", "203.0.113.0/24"},
		"mas de una semana":        {"2026-10-05T12:00:01Z", "203.0.113.0/24"},
		"sin rangos":               {"2026-09-27T18:00:00Z", ""},
		"rango mal escrito":        {"2026-09-27T18:00:00Z", "203.0.113.7"},
		"rango demasiado ancho":    {"2026-09-27T18:00:00Z", "203.0.113.0/23"},
		"ipv6 demasiado ancho":     {"2026-09-27T18:00:00Z", "2001:db8::/64"},
		"un rango bueno y otro no": {"2026-09-27T18:00:00Z", "203.0.113.0/24,nada"},
	}
	for name, c := range cases {
		setPassEnv(t, c[0], c[1])
		if _, err := loadAutomationPass(clock); err == nil {
			t.Errorf("%s: se acepto", name)
		}
	}
}

func TestPaseDeAutomatizacionSoloParaSusIPYMientrasDura(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	setPassEnv(t, "2026-09-27T18:00:00Z", " 203.0.113.0/24 , 2001:db8::1/128 ")
	pass, err := loadAutomationPass(clock)
	if err != nil {
		t.Fatal(err)
	}
	if !pass.allows("203.0.113.7") || !pass.allows("2001:db8::1") {
		t.Fatal("las IP del pase entran")
	}
	if pass.allows("198.51.100.1") || pass.allows("") || pass.allows("no-es-ip") {
		t.Fatal("cualquier otra cosa no")
	}
	now = now.Add(7 * time.Hour)
	if pass.open() || pass.allows("203.0.113.7") {
		t.Fatal("caducado, el pase se cierra solo")
	}
}

func TestPaseDeAutomatizacionMarcaElDocumentoSoloDeSusIP(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	setPassEnv(t, "2026-09-27T18:00:00Z", "203.0.113.0/24")
	pass, err := loadAutomationPass(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	doc := []byte("<!doctype html><html><head><title>x</title></head><body><div id=\"root\"></div></body></html>")
	reqFrom := func(ip string) *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/login", nil)
		return req.WithContext(context.WithValue(req.Context(), middleware.CtxClientIP, ip))
	}
	marked := string(pass.decorate(reqFrom("203.0.113.7"), doc))
	if !strings.Contains(marked, automationPassMeta+"</head>") || strings.Count(marked, "<meta") != 1 {
		t.Fatalf("la etiqueta va justo antes de </head>: %s", marked)
	}
	if got := string(pass.decorate(reqFrom("198.51.100.1"), doc)); got != string(doc) {
		t.Fatalf("otra IP recibe el documento intacto: %s", got)
	}
	if got := string(pass.decorate(reqFrom("203.0.113.7"), []byte("no es html"))); got != "no es html" {
		t.Fatal("sin </head> no se toca nada")
	}
}
