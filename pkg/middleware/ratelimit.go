package middleware

import (
	"net/http"
	"sync"
	"time"
)

type RateLimiter struct {
	mu       sync.Mutex
	visitors map[string]*visitor
	rate     int
	window   time.Duration
}

type visitor struct {
	count    int
	lastSeen time.Time
}

// minWindow acota por abajo la ventana del limitador. Una ventana expresada como numero
// suelto (NewRateLimiter(100, 60)) no son 60 segundos sino 60 NANOSEGUNDOS: el bucle de
// limpieza pasa a girar millones de veces por segundo tomando el mutex, y el proceso se
// come un nucleo entero sin hacer nada. Ocurrio: un servicio estuvo 30 dias al 100% de CPU
// por ese unico caracter. Ademas una ventana asi vuelve inutil el limite, porque cada
// visitante caduca antes de la siguiente peticion.
const minWindow = time.Second

func NewRateLimiter(rate int, window time.Duration) *RateLimiter {
	if window < minWindow {
		window = minWindow
	}
	rl := &RateLimiter{
		visitors: make(map[string]*visitor),
		rate:     rate,
		window:   window,
	}
	go rl.cleanup()
	return rl
}

// LimitPerUser acota por IDENTIDAD y no por direccion de salida.
//
// El limite por IP es correcto para lo que no esta autenticado, pero dentro de
// una oficina todo el mundo comparte una IP publica: el cupo se reparte entre
// todos y basta con que unas cuantas personas trabajen a la vez para que se
// devuelvan 429 sin que nadie haya hecho nada anomalo. Es una denegacion de
// servicio que se causa la propia empresa.
//
// Cuando no hay usuario en el contexto -- rutas publicas, servicio a servicio --
// se cae a la IP, que ahi si es la unica identidad disponible.
func (rl *RateLimiter) LimitPerUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clave := GetUserID(r.Context())
		if clave == "" {
			clave = extractIP(r)
		}
		if !rl.allow(clave) {
			w.Header().Set("Retry-After", "60")
			http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (rl *RateLimiter) Limit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := extractIP(r)
		if !rl.allow(ip) {
			w.Header().Set("Retry-After", "60")
			http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (rl *RateLimiter) allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	v, exists := rl.visitors[key]
	if !exists || time.Since(v.lastSeen) > rl.window {
		rl.visitors[key] = &visitor{count: 1, lastSeen: time.Now()}
		return true
	}

	if v.count >= rl.rate {
		return false
	}

	v.count++
	return true
}

func (rl *RateLimiter) cleanup() {
	for {
		time.Sleep(rl.window * 2)
		rl.mu.Lock()
		for key, v := range rl.visitors {
			if time.Since(v.lastSeen) > rl.window*2 {
				delete(rl.visitors, key)
			}
		}
		rl.mu.Unlock()
	}
}

func extractIP(r *http.Request) string {
	// En el gateway CaptureClientIP ya resolvio la IP real del visitante (validando
	// que X-Real-IP venga del proxy de borde de confianza); esa es la autoridad.
	if ip := GetClientIP(r.Context()); ip != "" {
		return ip
	}
	// En los servicios internos X-Real-IP lo pone el gateway (el cliente no puede
	// inyectarlo: el gateway lo consume y lo re-emite desde el contexto).
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		for i := 0; i < len(xff); i++ {
			if xff[i] == ',' {
				return xff[:i]
			}
		}
		return xff
	}
	for i := len(r.RemoteAddr) - 1; i >= 0; i-- {
		if r.RemoteAddr[i] == ':' {
			return r.RemoteAddr[:i]
		}
	}
	return r.RemoteAddr
}
