package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/go-chi/chi/v5"
)

// La pagina publica de citas va al webmail por la celda del enlace; la reserva lleva el cupo estricto por IP.
func TestLaPaginaDeCitasEsPublicaYLaReservaLlevaCupoEstricto(t *testing.T) {
	t.Setenv("GATEWAY_ROUTES_FILE", "")
	tbl, err := loadRouteTable()
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]publicRouteSpec{}
	for _, p := range tbl.Public {
		if strings.HasPrefix(p.Path, "/public/booking/") {
			found[p.Method] = p
		}
	}
	get, post := found[http.MethodGet], found[http.MethodPost]
	if get.Service != "webmail" || post.Service != "webmail" || get.Path != "/public/booking/{cell}/{tenant}/{page}" || post.Path != get.Path {
		t.Fatalf("rutas de citas: %+v", found)
	}
	if get.Limit != "" || post.Limit != publicLimitStrict {
		t.Fatalf("cupos: GET %q, POST %q", get.Limit, post.Limit)
	}
	bad := &routeTable{Services: tbl.Services, Public: []publicRouteSpec{{Method: "POST", Path: "/public/x/{cell}", Service: "webmail", Limit: "ilimitado"}}}
	if err := bad.validate(); err == nil || !strings.Contains(err.Error(), "limite") {
		t.Fatalf("un limite desconocido no se admite: %v", err)
	}
}

// El cupo estricto se suma al general: corta antes y no libra a la ruta del general.
func TestCupoEstrictoDeUnaRutaPublica(t *testing.T) {
	tbl := &routeTable{
		Services: map[string]serviceSpec{"x": {HostEnv: "X_HOST", DefaultHost: "x", DefaultPort: "80"}},
		Public: []publicRouteSpec{
			{Method: "POST", Path: "/public/citas/{page}", Service: "x", Limit: publicLimitStrict},
			{Method: "GET", Path: "/public/citas/{page}", Service: "x"},
		},
		upstreams: map[string]string{},
	}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	t.Cleanup(backend.Close)
	tbl.upstreams["x"] = backend.URL
	general := middleware.NewRateLimiter(4, time.Minute)
	strict := middleware.NewRateLimiter(2, time.Minute)

	r := chi.NewRouter()
	r.Route("/api/v1", func(r chi.Router) {
		r.Use(exceptWebhooks(tbl, general.Limit))
		mountPublic(r, tbl, "token-interno", publicLimits{webhook: passThrough, strict: strict.Limit})
	})
	do := func(method string) int {
		req := httptest.NewRequest(method, "/api/v1/public/citas/p1", nil)
		req.RemoteAddr = "203.0.113.9:4000"
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec.Code
	}
	for i, want := range []int{http.StatusNoContent, http.StatusNoContent, http.StatusTooManyRequests} {
		if code := do(http.MethodPost); code != want {
			t.Fatalf("reserva %d: %d, se esperaba %d", i+1, code, want)
		}
	}
	if code := do(http.MethodGet); code != http.StatusNoContent {
		t.Fatalf("la consulta no gasta el cupo estricto: %d", code)
	}
	if code := do(http.MethodGet); code != http.StatusTooManyRequests {
		t.Fatalf("todo sigue con el cupo general: %d", code)
	}
}
