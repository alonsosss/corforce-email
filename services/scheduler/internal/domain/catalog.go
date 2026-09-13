package domain

import (
	"fmt"
	"regexp"
	"sort"
)

// HandlerScope es el tipo de trabajo que un manejador acepta.
type HandlerScope string

const (
	ScopeTenant   HandlerScope = "tenant"
	ScopePlatform HandlerScope = "platform"
)

// MaxHandlerTimeoutSeconds es el techo del plazo de un manejador: la retencion del stream
// SCHEDULER (7 dias). Un ejecutor que tardase mas ya no podria releer el evento que lo lanzo.
const MaxHandlerTimeoutSeconds = 7 * 24 * 60 * 60

// HandlerSpec declara un manejador de trabajos: quien lo ejecuta, cuanto puede tardar como
// mucho y que tipos de trabajo acepta.
type HandlerSpec struct {
	Name              string
	Service           string
	Description       string
	MaxTimeoutSeconds int
	Scopes            []HandlerScope
}

// Allows indica si el manejador acepta ese tipo de trabajo.
func (s HandlerSpec) Allows(scope HandlerScope) bool {
	for _, have := range s.Scopes {
		if have == scope {
			return true
		}
	}
	return false
}

// EffectiveTimeoutSeconds acota el plazo del trabajo por el maximo del manejador. Un
// trabajo sin plazo propio (cero) toma el maximo.
func (s HandlerSpec) EffectiveTimeoutSeconds(jobTimeoutSeconds int) int {
	if jobTimeoutSeconds <= 0 || jobTimeoutSeconds > s.MaxTimeoutSeconds {
		return s.MaxTimeoutSeconds
	}
	return jobTimeoutSeconds
}

// HandlerCatalog es la lista blanca de manejadores: un trabajo solo puede apuntar a uno de
// ellos. El scheduler no ejecuta nada; el catalogo existe para que cada ejecucion que
// despacha tenga un servicio que la recoja y la cierre.
type HandlerCatalog struct {
	specs  []HandlerSpec
	byName map[string]HandlerSpec
}

var (
	handlerNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$`)
	serviceNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
)

// maxHandlerNameLength es el ancho de job_definitions.handler.
const maxHandlerNameLength = 255

// NewHandlerCatalog valida el catalogo entero y falla ante la primera incoherencia: un
// catalogo mal escrito es un error de despliegue que tiene que verse al arrancar.
func NewHandlerCatalog(specs []HandlerSpec) (*HandlerCatalog, error) {
	c := &HandlerCatalog{byName: make(map[string]HandlerSpec, len(specs))}
	for _, s := range specs {
		if len(s.Name) > maxHandlerNameLength || !handlerNameRe.MatchString(s.Name) {
			return nil, fmt.Errorf("catalogo de manejadores: nombre invalido %q", s.Name)
		}
		if _, dup := c.byName[s.Name]; dup {
			return nil, fmt.Errorf("catalogo de manejadores: nombre repetido %q", s.Name)
		}
		if !serviceNameRe.MatchString(s.Service) {
			return nil, fmt.Errorf("catalogo de manejadores: %q con servicio invalido %q", s.Name, s.Service)
		}
		if s.MaxTimeoutSeconds <= 0 || s.MaxTimeoutSeconds > MaxHandlerTimeoutSeconds {
			return nil, fmt.Errorf("catalogo de manejadores: %q con max_timeout_seconds fuera de 1..%d", s.Name, MaxHandlerTimeoutSeconds)
		}
		if len(s.Scopes) == 0 {
			return nil, fmt.Errorf("catalogo de manejadores: %q sin scopes", s.Name)
		}
		seen := map[HandlerScope]bool{}
		for _, scope := range s.Scopes {
			if scope != ScopeTenant && scope != ScopePlatform {
				return nil, fmt.Errorf("catalogo de manejadores: %q con scope desconocido %q", s.Name, scope)
			}
			if seen[scope] {
				return nil, fmt.Errorf("catalogo de manejadores: %q repite el scope %q", s.Name, scope)
			}
			seen[scope] = true
		}
		spec := s
		spec.Scopes = append([]HandlerScope(nil), s.Scopes...)
		c.byName[s.Name] = spec
		c.specs = append(c.specs, spec)
	}
	sort.Slice(c.specs, func(i, j int) bool { return c.specs[i].Name < c.specs[j].Name })
	return c, nil
}

// Resolve devuelve el manejador si existe y acepta el tipo del trabajo.
func (c *HandlerCatalog) Resolve(name string, platformJob bool) (HandlerSpec, error) {
	if c == nil {
		return HandlerSpec{}, fmt.Errorf("%w: %q is not in the catalog", ErrHandlerNotAllowed, name)
	}
	spec, ok := c.byName[name]
	if !ok {
		return HandlerSpec{}, fmt.Errorf("%w: %q is not in the catalog", ErrHandlerNotAllowed, name)
	}
	scope := ScopeTenant
	if platformJob {
		scope = ScopePlatform
	}
	if !spec.Allows(scope) {
		return HandlerSpec{}, fmt.Errorf("%w: %q does not accept %s jobs", ErrHandlerNotAllowed, name, scope)
	}
	return spec, nil
}

// List devuelve el catalogo ordenado por nombre.
func (c *HandlerCatalog) List() []HandlerSpec {
	if c == nil {
		return nil
	}
	out := make([]HandlerSpec, len(c.specs))
	for i, s := range c.specs {
		s.Scopes = append([]HandlerScope(nil), s.Scopes...)
		out[i] = s
	}
	return out
}
