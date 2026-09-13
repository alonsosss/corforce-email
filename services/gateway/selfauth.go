package main

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// mountSelfAuthenticated monta los prefijos que autentica el propio servicio (tabla
// self_authenticated) dentro de /api/v1: sin JWT ni RBAC de la plataforma, con el
// limitador que ya tenga r y, en las rutas de strict_limit, tambien con strict.
//
// Se monta fuera de los grupos con JWT a proposito: esos grupos enrutan por sus propios
// prefijos y la validacion de la tabla impide que uno de ellos se declare aqui, asi que
// ninguna ruta gateada queda sin JWT por este camino.
func mountSelfAuthenticated(r chi.Router, specs []selfAuthSpec, serviceURL func(string) string, strict func(http.Handler) http.Handler, internalToken string) {
	for _, s := range specs {
		proxy := reverseProxyWith(serviceURL(s.Service), internalToken, true)
		limits := s.StrictLimit
		r.Route("/"+s.Prefix, func(r chi.Router) {
			r.Handle("/*", proxy)
			for _, l := range limits {
				// La ruta entera para el resto de metodos y el metodo atacable con el
				// limitador estricto (chi da prioridad al registro por metodo).
				r.Handle(l.Path, proxy)
				r.With(strict).Method(l.Method, l.Path, proxy)
			}
		})
	}
}
