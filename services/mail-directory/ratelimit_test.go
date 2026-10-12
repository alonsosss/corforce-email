package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
}

func codeOf(h http.Handler, method, target string) int {
	req := httptest.NewRequest(method, target, nil)
	req.RemoteAddr = "10.0.0.5:4000"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestInternalCallsCountPerMailboxNotPerOrigin(t *testing.T) {
	h := rateLimited()(okHandler())
	for i := 0; i < mailboxRatePerMinute; i++ {
		if c := codeOf(h, http.MethodGet, "/internal/mail-directory/primary-senders?username=bea@acme.test"); c != http.StatusNoContent {
			t.Fatalf("peticion %d: %d", i, c)
		}
	}
	if c := codeOf(h, http.MethodGet, "/internal/mail-directory/primary-senders?username=bea@acme.test"); c != http.StatusTooManyRequests {
		t.Fatalf("el buzon que agota su cupo debe recibir 429, recibio %d", c)
	}
	// Otro buzon, desde el mismo origen, no se ve afectado.
	if c := codeOf(h, http.MethodGet, "/internal/mail-directory/primary-senders?username=ana@acme.test"); c != http.StatusNoContent {
		t.Fatalf("otro buzon desde el mismo origen: %d", c)
	}
	// El nombre del buzon no distingue mayusculas.
	if c := codeOf(h, http.MethodGet, "/internal/mail-directory/reminders?username=BEA@acme.test"); c != http.StatusTooManyRequests {
		t.Fatalf("mismo buzon con otra grafia: %d", c)
	}
}

func TestInternalCallsWithoutMailboxCountPerRoute(t *testing.T) {
	h := rateLimited()(okHandler())
	for i := 0; i < routeRatePerMinute; i++ {
		if c := codeOf(h, http.MethodPost, "/internal/mail-directory/reminders/claim"); c != http.StatusNoContent {
			t.Fatalf("peticion %d: %d", i, c)
		}
	}
	if c := codeOf(h, http.MethodPost, "/internal/mail-directory/reminders/claim"); c != http.StatusTooManyRequests {
		t.Fatalf("la ruta agotada debe dar 429, dio %d", c)
	}
	if c := codeOf(h, http.MethodPost, "/internal/mail-directory/scheduled-sends/claim"); c != http.StatusNoContent {
		t.Fatalf("otra ruta no comparte cupo: %d", c)
	}
}
