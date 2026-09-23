package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/go-chi/chi/v5"
)

func TestLosEventosDeSESLlevanCupoDeWebhook(t *testing.T) {
	t.Setenv("GATEWAY_ROUTES_FILE", "")
	tbl, err := loadRouteTable()
	if err != nil {
		t.Fatal(err)
	}
	marcadas := map[string]bool{}
	for _, p := range tbl.Public {
		if p.Limit == publicLimitWebhook {
			marcadas[p.Path] = true
		}
	}
	for _, path := range []string{"/public/transactional/ses-events", "/public/transactional/ses-events/{tenantID}"} {
		if !marcadas[path] {
			t.Errorf("%s debe ir con el cupo de webhooks", path)
		}
	}
	if marcadas["/public/transactional/unsubscribe"] {
		t.Error("la baja la pulsa una persona: va con el cupo general")
	}
}

// Los webhooks no gastan el cupo general ni lo sufren; tienen el suyo, que tambien corta.
func TestCupoDeWebhookSeparadoDelGeneral(t *testing.T) {
	tbl := &routeTable{Public: []publicRouteSpec{
		{Method: "POST", Path: "/public/proveedor/eventos/{tenantID}", Service: "x", Limit: publicLimitWebhook},
		{Method: "POST", Path: "/public/proveedor/baja", Service: "x"},
	}}
	general := middleware.NewRateLimiter(1, time.Minute)
	webhook := middleware.NewRateLimiter(3, time.Minute)
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

	r := chi.NewRouter()
	r.Route("/api/v1", func(r chi.Router) {
		r.Use(exceptWebhooks(tbl, general.Limit))
		r.With(webhook.Limit).Post("/public/proveedor/eventos/{tenantID}", ok)
		r.Post("/public/proveedor/baja", ok)
	})
	do := func(path string) int {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.RemoteAddr = "203.0.113.7:4000"
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec.Code
	}

	for i := 1; i <= 3; i++ {
		if code := do("/api/v1/public/proveedor/eventos/e" + string(rune('0'+i))); code != http.StatusNoContent {
			t.Fatalf("evento %d dentro del cupo de webhooks: %d", i, code)
		}
	}
	if code := do("/api/v1/public/proveedor/eventos/e4"); code != http.StatusTooManyRequests {
		t.Fatalf("el cupo de webhooks tambien corta: %d", code)
	}
	if code := do("/api/v1/public/proveedor/baja"); code != http.StatusNoContent {
		t.Fatalf("los eventos no gastan el cupo general: %d", code)
	}
	if code := do("/api/v1/public/proveedor/baja"); code != http.StatusTooManyRequests {
		t.Fatalf("el resto sigue con el cupo general: %d", code)
	}
}

func TestSinWebhooksElCupoGeneralNoCambia(t *testing.T) {
	general := middleware.NewRateLimiter(1, time.Minute)
	h := exceptWebhooks(&routeTable{}, general.Limit)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	for i, want := range []int{http.StatusOK, http.StatusTooManyRequests} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/x", nil)
		req.RemoteAddr = "203.0.113.8:4000"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("peticion %d: %d, se esperaba %d", i+1, rec.Code, want)
		}
	}
}
