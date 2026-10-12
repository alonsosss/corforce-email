package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLimitByKeyCountsPerKeyAndAnswersWithTheErrorEnvelope(t *testing.T) {
	rl := NewRateLimiter(2, time.Minute)
	h := rl.LimitByKey(func(r *http.Request) string { return r.Header.Get("X-Buzon") }, "demasiadas peticiones del buzon")(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	call := func(buzon string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "10.0.0.5:4000"
		req.Header.Set("X-Buzon", buzon)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	for i := 0; i < 2; i++ {
		if c := call("a").Code; c != http.StatusNoContent {
			t.Fatalf("peticion %d: %d", i, c)
		}
	}
	rec := call("a")
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("tercera peticion: %d retry-after=%q", rec.Code, rec.Header().Get("Retry-After"))
	}
	var body struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error.Code != "RATE_LIMITED" || body.Error.Message == "" {
		t.Fatalf("cuerpo: %s (%v)", rec.Body.String(), err)
	}
	if c := call("b").Code; c != http.StatusNoContent {
		t.Fatalf("otra clave desde el mismo origen: %d", c)
	}
}
