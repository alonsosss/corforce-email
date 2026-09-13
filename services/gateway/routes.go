package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	// ReadPosts: consultas que viajan en POST porque llevan cuerpo (una comprobacion de
	// mil direcciones, un renderizado de plantilla). El RBAC las gatea como LECTURA del
	// modulo: exigir una accion de escritura dejaria fuera a quien solo puede consultar.
	// Se identifican por prefijo y ultimo segmento (`/api/v1/<prefix>/.../<action>`).
	ReadPosts []readPostSpec `json:"read_posts,omitempty"`
	// Public: rutas bajo /api/v1 que se sirven SIN sesion (webhooks de proveedores, bajas
	// de suscripcion desde el correo). Van con el limitador general y nada mas: la
	// proteccion es del propio servicio (firma del proveedor, enlace firmado). Metodo y
	// ruta exactos con la sintaxis de chi; el servicio recibe la misma ruta. Toda ruta de un
	// servicio de celda (cell_hosts_env) lleva el segmento {cell} y se enruta por el
	// (cells.go).
	Public []publicRouteSpec `json:"public,omitempty"`
	// SelfAuthenticated: prefijos bajo /api/v1 cuyo servicio autentica cada peticion con
	// su propia sesion (el webmail, con la cookie del buzon: sus usuarios son buzones, no
	// usuarios de la plataforma). El gateway no exige JWT ni aplica RBAC por modulo, pero
	// si el limitador general, las cabeceras de seguridad y el token interno hacia el
	// servicio. StrictLimit son las rutas atacables por fuerza bruta (el inicio de
	// sesion), que ademas pasan por el limitador de autenticacion.
	SelfAuthenticated []selfAuthSpec `json:"self_authenticated,omitempty"`
	// Frontend: servicio que sirve la aplicacion web (comodin /*). Opcional: sin el,
	// el gateway solo expone el API.
	Frontend string `json:"frontend,omitempty"`

	// cellTargets: servicio de celda -> celda -> URL de su instancia, leido del entorno al
	// cargar la tabla (loadCellTargets).
	cellTargets map[string]map[string]string
}

type selfAuthSpec struct {
	Prefix      string           `json:"prefix"`
	Service     string           `json:"service"`
	StrictLimit []methodPathSpec `json:"strict_limit,omitempty"`
}

type methodPathSpec struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

// reservedPrefixes los monta el gateway por su cuenta: un prefijo autenticado por el
// servicio no puede ocuparlos.
var reservedPrefixes = map[string]bool{"auth": true, "public": true}

type readPostSpec struct {
	Prefix string `json:"prefix"`
	Action string `json:"action"`
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
	// CellHostsEnv marca un servicio desplegado por celda y nombra la variable con sus
	// instancias por celda ("celda=host:puerto,..."). El destino base (HostEnv) es el de la
	// celda por defecto; vacia, todas las celdas van a el (despliegue de una celda).
	CellHostsEnv string `json:"cell_hosts_env,omitempty"`
}

type routeSpec struct {
	Prefix  string `json:"prefix"`
	Service string `json:"service"`
	Module  string `json:"module"`
}

var (
	prefixRe  = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	moduleRe  = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
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
	t, err := decodeRouteTable(raw)
	if err != nil {
		return nil, err
	}
	if err := t.validate(); err != nil {
		return nil, err
	}
	if err := t.loadCellTargets(); err != nil {
		return nil, err
	}
	return t, nil
}

// decodeRouteTable rechaza los campos que la tabla no conoce: una clave mal escrita o que
// ya no existe se ignoraria en silencio y la ruta se comportaria distinto de lo que dice el
// fichero.
func decodeRouteTable(raw []byte) (*routeTable, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var t routeTable
	if err := dec.Decode(&t); err != nil {
		return nil, fmt.Errorf("tabla de rutas: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("tabla de rutas: contenido despues del objeto")
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
	if err := t.validateCellHostsEnv(); err != nil {
		return err
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
	for _, rp := range t.ReadPosts {
		if !seen[rp.Prefix] || !prefixRe.MatchString(rp.Action) {
			return fmt.Errorf("tabla de rutas: read_post invalido %q/%q", rp.Prefix, rp.Action)
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
		if err := t.validatePublicCell(p); err != nil {
			return err
		}
	}
	if err := t.validateCellServicesRouted(); err != nil {
		return err
	}
	for _, s := range t.SelfAuthenticated {
		if !prefixRe.MatchString(s.Prefix) {
			return fmt.Errorf("tabla de rutas: prefijo autenticado por el servicio invalido %q", s.Prefix)
		}
		// Un prefijo que ya tiene ruta con JWT perderia su gateo si se declarara aqui.
		if seen[s.Prefix] || reservedPrefixes[s.Prefix] {
			return fmt.Errorf("tabla de rutas: el prefijo autenticado por el servicio %q choca con otra ruta", s.Prefix)
		}
		seen[s.Prefix] = true
		if _, ok := t.Services[s.Service]; !ok {
			return fmt.Errorf("tabla de rutas: el prefijo %q apunta al servicio desconocido %q", s.Prefix, s.Service)
		}
		for _, l := range s.StrictLimit {
			switch l.Method {
			case "GET", "POST", "PUT", "PATCH", "DELETE":
			default:
				return fmt.Errorf("tabla de rutas: metodo invalido %q en strict_limit de %q", l.Method, s.Prefix)
			}
			if !strings.HasPrefix(l.Path, "/") || strings.Contains(l.Path, "..") {
				return fmt.Errorf("tabla de rutas: ruta invalida %q en strict_limit de %q", l.Path, s.Prefix)
			}
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

// readPostIndex devuelve prefijo -> acciones POST que se gatean como lectura.
func (t *routeTable) readPostIndex() map[string]map[string]bool {
	m := make(map[string]map[string]bool)
	for _, rp := range t.ReadPosts {
		if m[rp.Prefix] == nil {
			m[rp.Prefix] = map[string]bool{}
		}
		m[rp.Prefix][rp.Action] = true
	}
	return m
}

// lastSegment devuelve el ultimo segmento de la ruta sin barra final.
func lastSegment(path string) string {
	p := strings.TrimSuffix(path, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
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
