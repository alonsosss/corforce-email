package main

import (
	"net/http"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/middleware"
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
		perMailbox := mailbox.LimitByKey(internalMailboxKey, "demasiadas peticiones del buzon")(next)
		perRoute := route.LimitByKey(internalRouteKey, "demasiadas peticiones")(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case !strings.HasPrefix(r.URL.Path, internalPrefix):
				perSession.ServeHTTP(w, r)
			case internalMailbox(r) != "":
				perMailbox.ServeHTTP(w, r)
			default:
				perRoute.ServeHTTP(w, r)
			}
		})
	}
}

// internalMailbox es el buzon que nombra la llamada interna, en minusculas, o vacio si no nombra ninguno.
func internalMailbox(r *http.Request) string {
	return strings.ToLower(strings.TrimSpace(r.URL.Query().Get("username")))
}

func internalMailboxKey(r *http.Request) string { return "mailbox:" + internalMailbox(r) }

// internalRouteKey agrupa por metodo y recurso (sin identificadores), para que el conjunto de
// claves sea finito.
func internalRouteKey(r *http.Request) string {
	rest := strings.TrimPrefix(r.URL.Path, internalPrefix)
	resource, _, _ := strings.Cut(rest, "/")
	return "route:" + r.Method + ":" + resource
}
