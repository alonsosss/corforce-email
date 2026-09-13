package main

import (
	"net/http"

	"github.com/alonsosss/corforce-email/pkg/tenantcell"
	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

// mountSelfAuthenticated monta los prefijos que autentica el propio servicio (tabla
// self_authenticated) dentro de /api/v1: sin JWT ni RBAC de la plataforma, con el
// limitador que ya tenga r y, en las rutas de strict_limit, tambien con strict. Los de un
// servicio de celda se enrutan por celda (selfauthcells.go); domains resuelve la celda de un
// dominio de correo y es nil exactamente cuando el despliegue es de una celda.
//
// Se monta fuera de los grupos con JWT a proposito: esos grupos enrutan por sus propios
// prefijos y la validacion de la tabla impide que uno de ellos se declare aqui, asi que
// ninguna ruta gateada queda sin JWT por este camino.
func mountSelfAuthenticated(r chi.Router, t *routeTable, strict func(http.Handler) http.Handler, internalToken string, domains *tenantcell.Resolver, logger *zap.Logger) {
	for _, s := range t.SelfAuthenticated {
		session, login := selfAuthHandlers(t, s, internalToken, domains, logger)
		r.Route("/"+s.Prefix, func(r chi.Router) {
			r.Handle("/*", session)
			for _, l := range s.StrictLimit {
				// La ruta entera para el resto de metodos y el metodo atacable con el
				// limitador estricto (chi da prioridad al registro por metodo). El inicio de
				// sesion por celda se enruta ademas por el dominio del buzon.
				h := session
				if s.CellLogin != nil && l.Method == s.CellLogin.Method && l.Path == s.CellLogin.Path {
					h = login
				}
				r.Handle(l.Path, session)
				r.With(strict).Method(l.Method, l.Path, h)
			}
		})
	}
}
