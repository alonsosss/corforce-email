package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func peticionDe(userID, ip string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = ip + ":1234"
	if userID != "" {
		r.Header.Set("X-User-ID", userID)
		r = r.WithContext(context.WithValue(r.Context(), CtxUserID, userID))
	}
	return r
}

func consumir(t *testing.T, h http.Handler, r *http.Request, veces int) int {
	t.Helper()
	ultimo := 0
	for i := 0; i < veces; i++ {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		ultimo = w.Code
	}
	return ultimo
}

// El caso que motiva el cambio: una oficina entera sale por la misma IP y no
// debe repartirse un unico cupo entre todos sus trabajadores.
func TestLimitPerUserNoLoReparteEntreCompaneros(t *testing.T) {
	rl := NewRateLimiter(3, time.Minute)
	h := rl.LimitPerUser(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))

	// La primera persona agota su cupo.
	if code := consumir(t, h, peticionDe("ana", "203.0.113.7"), 4); code != http.StatusTooManyRequests {
		t.Fatalf("el cupo propio no se agoto: %d", code)
	}
	// Su companero, tras la MISMA IP, debe seguir trabajando.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, peticionDe("luis", "203.0.113.7"))
	if w.Code != http.StatusOK {
		t.Fatalf("un companero tras la misma IP quedo bloqueado por el consumo de otro: %d", w.Code)
	}
}

// Sin usuario en el contexto no hay otra identidad que la direccion, y ahi el
// limite tiene que seguir aplicandose.
func TestLimitPerUserCaeALaIPSinUsuario(t *testing.T) {
	rl := NewRateLimiter(2, time.Minute)
	h := rl.LimitPerUser(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))

	if code := consumir(t, h, peticionDe("", "198.51.100.4"), 3); code != http.StatusTooManyRequests {
		t.Fatalf("sin usuario, el limite por IP dejo de aplicarse: %d", code)
	}
}

// El limite sigue existiendo por persona: no es una puerta abierta.
func TestLimitPerUserSigueAcotandoACadaUno(t *testing.T) {
	rl := NewRateLimiter(2, time.Minute)
	h := rl.LimitPerUser(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))

	if code := consumir(t, h, peticionDe("ana", "203.0.113.7"), 3); code != http.StatusTooManyRequests {
		t.Fatalf("una sola persona pudo superar su cupo: %d", code)
	}
}
