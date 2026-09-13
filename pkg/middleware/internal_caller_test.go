package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequireInternalCallerRechazaAUnaPersona(t *testing.T) {
	reached := 0
	h := InjectFromGateway(RequireInternalCaller(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached++
		w.WriteHeader(http.StatusNoContent)
	})))

	req := httptest.NewRequest(http.MethodPut, "/internal/x", nil)
	req.Header.Set("X-User-ID", "3f1c1e8e-8f3a-4a57-9d49-0f7c8e0f9a11")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || reached != 0 {
		t.Fatalf("con usuario: %d (alcanzado %d veces), se esperaba 403 sin llegar al handler", rec.Code, reached)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/internal/x", nil))
	if rec.Code != http.StatusNoContent || reached != 1 {
		t.Fatalf("sin usuario: %d, se esperaba que pasara", rec.Code)
	}
}
