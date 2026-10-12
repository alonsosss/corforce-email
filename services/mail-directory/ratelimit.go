package main

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
)

const (
	internalPrefix = "/internal/mail-directory/"

	// El API con sesion cuenta por usuario (llega por el gateway, donde la IP es siempre la misma).
	sessionRatePerMinute = 120
	// La API interna cuenta por buzon: todo el webmail, mail-auth y los demas servicios llegan desde
	// unas pocas IP, y un cupo por origen se lo reparten todos los buzones y los sondeos de fondo.
	mailboxRatePerMinute = 120
	// Lo que no lleva buzon (altas de dominio, reclamos de los sondeos) cuenta por ruta, para que un
	// sondeo no gaste el cupo de otra operacion.
	routeRatePerMinute = 600
)

// rateLimited aplica el cupo que corresponde a cada tipo de llamada del servicio.
func rateLimited() func(http.Handler) http.Handler {
	session := middleware.NewRateLimiter(sessionRatePerMinute, time.Minute)
	mailbox := middleware.NewRateLimiter(mailboxRatePerMinute, time.Minute)
	route := middleware.NewRateLimiter(routeRatePerMinute, time.Minute)
	return func(next http.Handler) http.Handler {
		perSession := session.LimitPerUser(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasPrefix(r.URL.Path, internalPrefix) {
				perSession.ServeHTTP(w, r)
				return
			}
			rl, key := route, internalRouteKey(r)
			if username := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("username"))); username != "" {
				rl, key = mailbox, "mailbox:"+username
			}
			if ok, reset := rl.AllowKey(r.Context(), key); !ok {
				w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(reset.Seconds())))))
				response.Err(w, http.StatusTooManyRequests, "RATE_LIMITED", "demasiadas peticiones")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// internalRouteKey agrupa por metodo y recurso (sin identificadores), para que el conjunto de
// claves sea finito.
func internalRouteKey(r *http.Request) string {
	rest := strings.TrimPrefix(r.URL.Path, internalPrefix)
	resource, _, _ := strings.Cut(rest, "/")
	return "route:" + r.Method + ":" + resource
}
