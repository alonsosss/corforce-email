package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// La tabla de enrutado vive en datos, no en codigo. En la plataforma de la que
// nace este gateway cada servicio nuevo obligaba a recompilarlo y a mantener a
// mano tres listas (URL, ruta y modulo de permisos); olvidar la tercera dejaba
// las escrituras del servicio sin gatear y nada lo delataba. Aqui una ruta se
// declara una sola vez con su servicio y su modulo, y el gateway se niega a
// arrancar si la declaracion es incoherente.
//
// routes.json es la tabla por defecto, embebida en el binario; GATEWAY_ROUTES_FILE
// la sustituye entera (no la mezcla) para un despliegue con otra topologia.
//
//go:embed routes.json
var defaultRoutes []byte

// routeTable es el contrato del fichero de rutas.
type routeTable struct {
	// Services: nombre logico -> como localizarlo. El host y el puerto reales
	// salen del entorno (<HostEnv> y <HostEnv>_PORT) con estos valores por defecto,
	// que son los del compose de desarrollo.
	Services map[string]serviceSpec `json:"services"`
	// Routes: prefijo bajo /api/v1 -> servicio y modulo de permisos que lo gatea.
	// Un modulo vacio significa "no se gatea por modulo" y solo se admite en las
	// rutas de consulta de acceso, que ya resuelve el propio servicio con el JWT.
	Routes []routeSpec `json:"routes"`
	// Public: rutas bajo /api/v1 que se sirven SIN sesion (webhooks de proveedores, bajas
	// de suscripcion desde el correo). Van con el limitador general y nada mas: la
	// proteccion es del propio servicio (firma del proveedor, enlace firmado). Metodo y
	// ruta exactos con la sintaxis de chi; el servicio recibe la misma ruta.
	Public []publicRouteSpec `json:"public,omitempty"`
	// Frontend: servicio que sirve la aplicacion web (comodin /*). Opcional: sin el,
	// el gateway solo expone el API.
	Frontend string `json:"frontend,omitempty"`
}

type publicRouteSpec struct {
	Method  string `json:"method"`
	Path    string `json:"path"`
	Service string `json:"service"`
}

type serviceSpec struct {
	HostEnv     string `json:"host_env"`
	DefaultHost string `json:"default_host"`
	DefaultPort string `json:"default_port"`
}

type routeSpec struct {
	Prefix  string `json:"prefix"`
	Service string `json:"service"`
	Module  string `json:"module"`
}

var (
	prefixRe = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	moduleRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	hostEnvRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
)

// loadRouteTable lee y valida la tabla. Falla en vez de degradar: una ruta sin
// servicio o un prefijo repetido es un error de despliegue que hay que ver al
// arrancar, no un 502 en produccion.
func loadRouteTable() (*routeTable, error) {
	raw := defaultRoutes
	if path := os.Getenv("GATEWAY_ROUTES_FILE"); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("leer GATEWAY_ROUTES_FILE: %w", err)
		}
		raw = b
	}
	var t routeTable
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, fmt.Errorf("tabla de rutas: %w", err)
	}
	if err := t.validate(); err != nil {
		return nil, err
	}
	return &t, nil
}

func (t *routeTable) validate() error {
	if len(t.Services) == 0 {
		return fmt.Errorf("tabla de rutas: sin servicios")
	}
	for name, s := range t.Services {
		if !prefixRe.MatchString(name) {
			return fmt.Errorf("tabla de rutas: nombre de servicio invalido %q", name)
		}
		if !hostEnvRe.MatchString(s.HostEnv) || s.DefaultHost == "" || s.DefaultPort == "" {
			return fmt.Errorf("tabla de rutas: servicio %q incompleto (host_env, default_host, default_port)", name)
		}
	}
	seen := make(map[string]bool, len(t.Routes))
	for _, r := range t.Routes {
		if !prefixRe.MatchString(r.Prefix) {
			return fmt.Errorf("tabla de rutas: prefijo invalido %q", r.Prefix)
		}
		if seen[r.Prefix] {
			return fmt.Errorf("tabla de rutas: prefijo repetido %q", r.Prefix)
		}
		seen[r.Prefix] = true
		if _, ok := t.Services[r.Service]; !ok {
			return fmt.Errorf("tabla de rutas: la ruta %q apunta al servicio desconocido %q", r.Prefix, r.Service)
		}
		if r.Module != "" && !moduleRe.MatchString(r.Module) {
			return fmt.Errorf("tabla de rutas: modulo invalido %q en %q", r.Module, r.Prefix)
		}
	}
	for _, p := range t.Public {
		switch p.Method {
		case "GET", "POST", "PUT", "DELETE":
		default:
			return fmt.Errorf("tabla de rutas: metodo publico invalido %q", p.Method)
		}
		if !strings.HasPrefix(p.Path, "/public/") {
			return fmt.Errorf("tabla de rutas: la ruta publica %q debe colgar de /public/", p.Path)
		}
		if _, ok := t.Services[p.Service]; !ok {
			return fmt.Errorf("tabla de rutas: la ruta publica %q apunta al servicio desconocido %q", p.Path, p.Service)
		}
	}
	if t.Frontend != "" {
		if _, ok := t.Services[t.Frontend]; !ok {
			return fmt.Errorf("tabla de rutas: frontend %q no esta en services", t.Frontend)
		}
	}
	return nil
}

// serviceURL resuelve la URL interna de un servicio: el entorno manda y los
// valores del fichero son el respaldo de desarrollo.
func (t *routeTable) serviceURL(name string) string {
	s := t.Services[name]
	host := os.Getenv(s.HostEnv)
	if host == "" {
		host = s.DefaultHost
	}
	port := os.Getenv(s.HostEnv + "_PORT")
	if port == "" {
		port = s.DefaultPort
	}
	return "http://" + host + ":" + port
}

// moduleIndex devuelve el mapa prefijo -> modulo de permisos que usa el RBAC y
// el rastro de auditoria. Un prefijo sin modulo no aparece.
func (t *routeTable) moduleIndex() map[string]string {
	m := make(map[string]string, len(t.Routes))
	for _, r := range t.Routes {
		if r.Module != "" {
			m[r.Prefix] = r.Module
		}
	}
	return m
}

// pathSegments devuelve los dos primeros segmentos de /api/v1/<seg1>/<seg2>/...
// seg1 identifica el modulo; seg2 permite detectar autoservicio (".../me/...").
func pathSegments(path string) (seg1, seg2 string) {
	p := strings.TrimPrefix(path, "/api/v1/")
	parts := strings.SplitN(p, "/", 3)
	if len(parts) > 0 {
		seg1 = parts[0]
	}
	if len(parts) > 1 {
		seg2 = parts[1]
	}
	return seg1, seg2
}
