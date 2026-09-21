package main

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
)

// WebDAV (RFC 4918) usa metodos que chi no conoce. Se registran para poder enrutarlos, pero solo
// pasan donde la tabla de rutas los declara: un prefijo autenticado por el servicio los lista en
// "methods" (mail-dav, CardDAV y CalDAV) y una ruta de descubrimiento (well_known) responde a todos con la
// redireccion. En cualquier otro sitio -las rutas con JWT, las publicas, la aplicacion- el gateway
// responde 405 antes de enrutar: el RBAC clasifica como lectura todo lo que no sea POST, PUT,
// PATCH o DELETE, y un MKCOL, un MKCALENDAR o un MOVE no puede colarse como una lectura de un modulo.
var extensionMethods = map[string]bool{
	"PROPFIND": true, "PROPPATCH": true, "REPORT": true, "MKCOL": true, "MKCALENDAR": true,
	"COPY": true, "MOVE": true, "LOCK": true, "UNLOCK": true,
}

func init() {
	for m := range extensionMethods {
		chi.RegisterMethod(m)
	}
}

// wellKnownSpec es una ruta de descubrimiento (RFC 6764) que redirige al prefijo de un servicio
// autenticado por el servicio: los clientes de contactos la piden en la raiz del dominio.
type wellKnownSpec struct {
	Path   string `json:"path"`
	Prefix string `json:"prefix"`
}

var wellKnownPathRe = regexp.MustCompile(`^/\.well-known/[a-z][a-z0-9-]*$`)

// apiPrefix es el prefijo bajo el que el gateway monta todo el API.
const apiPrefix = "/api/v1/"

func (t *routeTable) validateWellKnown() error {
	selfAuth := make(map[string]bool, len(t.SelfAuthenticated))
	for _, s := range t.SelfAuthenticated {
		selfAuth[s.Prefix] = true
		for _, m := range s.Methods {
			if !extensionMethods[m] {
				return fmt.Errorf("tabla de rutas: metodo WebDAV invalido %q en el prefijo %q", m, s.Prefix)
			}
		}
	}
	seen := make(map[string]bool, len(t.WellKnown))
	for _, w := range t.WellKnown {
		if !wellKnownPathRe.MatchString(w.Path) {
			return fmt.Errorf("tabla de rutas: ruta de descubrimiento invalida %q", w.Path)
		}
		if seen[w.Path] {
			return fmt.Errorf("tabla de rutas: ruta de descubrimiento repetida %q", w.Path)
		}
		seen[w.Path] = true
		if !selfAuth[w.Prefix] {
			return fmt.Errorf("tabla de rutas: %q redirige al prefijo %q, que no esta en self_authenticated", w.Path, w.Prefix)
		}
	}
	return nil
}

// webdavGuard deja pasar un metodo de extension solo hacia un prefijo que lo declara o una ruta de
// descubrimiento. Va antes de cualquier ruta.
func webdavGuard(t *routeTable) func(http.Handler) http.Handler {
	byPrefix := make(map[string]map[string]bool, len(t.SelfAuthenticated))
	for _, s := range t.SelfAuthenticated {
		if len(s.Methods) == 0 {
			continue
		}
		set := make(map[string]bool, len(s.Methods))
		for _, m := range s.Methods {
			set[m] = true
		}
		byPrefix[apiPrefix+s.Prefix] = set
	}
	wellKnown := make(map[string]bool, len(t.WellKnown))
	for _, w := range t.WellKnown {
		wellKnown[w.Path] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// El mismo path con el que chi enruta (RawPath si existe): guardia y enrutado no pueden
			// leer dos rutas distintas de una misma peticion.
			path := r.URL.EscapedPath()
			if !extensionMethods[r.Method] || wellKnown[path] {
				next.ServeHTTP(w, r)
				return
			}
			for prefix, methods := range byPrefix {
				if (path == prefix || strings.HasPrefix(path, prefix+"/")) && methods[r.Method] {
					next.ServeHTTP(w, r)
					return
				}
			}
			http.Error(w, "metodo no admitido", http.StatusMethodNotAllowed)
		})
	}
}

// mountWellKnown monta las rutas de descubrimiento. La redireccion es permanente (301): los clientes
// de contactos la siguen con el mismo metodo y con sus credenciales.
func mountWellKnown(r chi.Router, t *routeTable, limit func(http.Handler) http.Handler) {
	for _, w := range t.WellKnown {
		target := apiPrefix + w.Prefix + "/"
		r.With(limit).Handle(w.Path, http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
			rw.Header().Set("Cache-Control", "no-store")
			http.Redirect(rw, req, target, http.StatusMovedPermanently)
		}))
	}
}
