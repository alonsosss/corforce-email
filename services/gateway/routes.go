package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/alonsosss/corforce-email/pkg/config"
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
	// que son los del compose de desarrollo; se validan todos al cargar la tabla.
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
	// ruta exactos con la sintaxis de chi; el servicio recibe la misma ruta. Toda ruta publica
	// de un servicio de celda (cell_hosts_env) lleva el segmento {cell} y se enruta por el
	// (cells.go).
	Public []publicRouteSpec `json:"public,omitempty"`
	// SelfAuthenticated: prefijos bajo /api/v1 cuyo servicio autentica cada peticion con
	// su propia sesion (el webmail, con la cookie del buzon: sus usuarios son buzones, no
	// usuarios de la plataforma). El gateway no exige JWT ni aplica RBAC por modulo, pero
	// si el limitador general, las cabeceras de seguridad y el token interno hacia el
	// servicio. StrictLimit son las rutas atacables por fuerza bruta (el inicio de
	// sesion), que ademas pasan por el limitador de autenticacion. Un prefijo de un servicio
	// de celda declara como se enruta por celda (CellLogin y CellCookie, selfauthcells.go).
	SelfAuthenticated []selfAuthSpec `json:"self_authenticated,omitempty"`
	// WellKnown: rutas de descubrimiento en la raiz del dominio que redirigen al prefijo de un
	// servicio autenticado por el servicio (webdav.go).
	WellKnown []wellKnownSpec `json:"well_known,omitempty"`
	// APIKeyRoutes: la lista cerrada de rutas con sesion que admiten, ademas del JWT, una clave
	// de API de empresa (Authorization: Bearer cfm_...). Metodo y ruta exactos con la sintaxis de
	// chi bajo /api/v1; cada una cuelga de un prefijo de routes con modulo, y el RBAC la gatea
	// con el alcance de la clave (apikeys.go).
	APIKeyRoutes []methodPathSpec `json:"api_key_routes,omitempty"`
	// Frontend: servicio que sirve la aplicacion web (comodin /*). Opcional: sin el,
	// el gateway solo expone el API.
	Frontend string `json:"frontend,omitempty"`

	// cellTargets: servicio de celda -> celda -> URL de su instancia, y baseCell: la celda que
	// sirven los destinos base (GATEWAY_BASE_CELL_CODE; vacia, despliegue de una celda). Se
	// leen del entorno al cargar la tabla (loadCellTargets).
	cellTargets map[string]map[string]string
	baseCell    string
	// upstreams: servicio -> URL de su destino base, resuelta del entorno al cargar la tabla
	// (loadUpstreams).
	upstreams map[string]string
}

type selfAuthSpec struct {
	Prefix      string           `json:"prefix"`
	Service     string           `json:"service"`
	StrictLimit []methodPathSpec `json:"strict_limit,omitempty"`
	// CellLogin es el inicio de sesion, que se enruta por el dominio del nombre de usuario que
	// lleva su cuerpo; CellCookie, la cookie de sesion cuyo token lleva la celda como prefijo,
	// por la que se enruta el resto. Solo en un servicio de celda, y los dos juntos.
	CellLogin  *cellLoginSpec `json:"cell_login,omitempty"`
	CellCookie string         `json:"cell_cookie,omitempty"`
	// Methods son los metodos WebDAV (PROPFIND, REPORT...) que el prefijo admite ademas de los
	// habituales; sin declararlos el gateway responde 405 (webdav.go).
	Methods []string `json:"methods,omitempty"`
}

type cellLoginSpec struct {
	Method        string `json:"method"`
	Path          string `json:"path"`
	UsernameField string `json:"username_field"`
}

type methodPathSpec struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

// ungatedPrefixes son las unicas rutas con sesion sin modulo de permisos: las consultas de acceso,
// que access-control resuelve con el JWT del propio usuario. Cualquier otra ruta debe declarar modulo.
var ungatedPrefixes = map[string]bool{"access": true, "check-access": true, "policy": true}

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
	// Limit "webhook" saca la ruta del cupo general por IP y la pasa por el de webhooks: un
	// proveedor (SNS) entrega desde pocas IP y a rafagas. Solo para rutas que autentica el
	// servicio con la firma del proveedor.
	Limit string `json:"limit,omitempty"`
	// Content "untrusted_html" declara que la ruta devuelve HTML escrito por un tercero (el
	// correo de una empresa visto en el navegador): el gateway conserva la CSP del servicio,
	// mas estricta que la suya, y no marca sus <script> con el nonce de la aplicacion.
	// "embeddable_html" es HTML de la plataforma que se incrusta en sitios de terceros (el
	// formulario de suscripcion): la CSP y el permiso de marco (frame-ancestors) son solo los del
	// servicio, sin los del borde, que prohiben todo marco; si el servicio no declara
	// frame-ancestors, el gateway pone una politica que no deja incrustar nada.
	Content string `json:"content,omitempty"`
	// CORS "service" deja la politica CORS al servicio (la decide por recurso, p. ej. los
	// origenes declarados de un formulario): el gateway no aplica la suya a la ruta y le pasa la
	// comprobacion previa (OPTIONS).
	CORS string `json:"cors,omitempty"`
	// Alias es una ruta en la raiz del dominio que sirve lo mismo (p. ej. /p/{tenant}/{slug} para
	// las paginas de aterrizaje). Solo GET y con los mismos parametros que la ruta.
	Alias string `json:"alias,omitempty"`
	// Transfer "download" declara que la ruta entrega un fichero grande (el enlace de un fichero
	// compartido): el gateway amplia su plazo de escritura (transfer.go) y el del servicio acota.
	Transfer string `json:"transfer,omitempty"`
}

// Valores admitidos en publicRouteSpec.
const (
	publicLimitWebhook          = "webhook"
	publicContentUntrustedHTML  = "untrusted_html"
	publicContentEmbeddableHTML = "embeddable_html"
	publicCORSService           = "service"
	publicTransferDownload      = "download"
)

// aliasRe: una ruta en la raiz con segmentos fijos o parametros enteros.
var (
	aliasRe       = regexp.MustCompile(`^(/([a-z0-9][a-z0-9-]*|\{[a-zA-Z]+\}))+$`)
	pathParamRe   = regexp.MustCompile(`\{([a-zA-Z]+)\}`)
	aliasReserved = []string{"/api", "/media", "/health", "/healthz", "/metrics", "/.well-known"}
)

// validatePublicExtras comprueba content, cors y alias de una ruta publica.
func validatePublicExtras(p publicRouteSpec) error {
	switch p.Content {
	case "":
	case publicContentUntrustedHTML:
		if p.Method != "GET" {
			return fmt.Errorf("tabla de rutas: contenido %q solo en GET (%s %q)", p.Content, p.Method, p.Path)
		}
	case publicContentEmbeddableHTML:
		if p.Method != "GET" && p.Method != "POST" {
			return fmt.Errorf("tabla de rutas: contenido %q solo en GET o POST (%s %q)", p.Content, p.Method, p.Path)
		}
	default:
		return fmt.Errorf("tabla de rutas: contenido %q invalido en la ruta publica %s %q (solo %q o %q)",
			p.Content, p.Method, p.Path, publicContentUntrustedHTML, publicContentEmbeddableHTML)
	}
	if p.CORS != "" && p.CORS != publicCORSService {
		return fmt.Errorf("tabla de rutas: cors %q invalido en la ruta publica %q (solo %q)", p.CORS, p.Path, publicCORSService)
	}
	if p.CORS != "" && p.Limit == publicLimitWebhook {
		return fmt.Errorf("tabla de rutas: la ruta publica %q no puede ser webhook y dejar CORS al servicio", p.Path)
	}
	if err := validatePublicTransfer(p); err != nil {
		return err
	}
	if p.Alias == "" {
		return nil
	}
	if p.Method != "GET" || !aliasRe.MatchString(p.Alias) || cellSegment(p.Path) {
		return fmt.Errorf("tabla de rutas: alias %q invalido para %s %q (solo GET, sin celda, con segmentos simples)", p.Alias, p.Method, p.Path)
	}
	for _, r := range aliasReserved {
		if p.Alias == r || strings.HasPrefix(p.Alias, r+"/") {
			return fmt.Errorf("tabla de rutas: el alias %q ocupa una ruta del gateway", p.Alias)
		}
	}
	params := func(s string) []string {
		var out []string
		for _, m := range pathParamRe.FindAllStringSubmatch(s, -1) {
			out = append(out, m[1])
		}
		slices.Sort(out)
		return out
	}
	if !slices.Equal(params(p.Alias), params(p.Path)) {
		return fmt.Errorf("tabla de rutas: el alias %q no lleva los mismos parametros que %q", p.Alias, p.Path)
	}
	return nil
}

type serviceSpec struct {
	HostEnv     string `json:"host_env"`
	DefaultHost string `json:"default_host"`
	DefaultPort string `json:"default_port"`
	// CellHostsEnv marca un servicio desplegado por celda, cuyas rutas se enrutan todas por
	// celda, y nombra la variable con sus instancias por celda ("celda=host:puerto,..."). El
	// destino base (HostEnv) sirve la celda GATEWAY_BASE_CELL_CODE; sin celdas declaradas ni
	// celda base, todas van a el (despliegue de una celda).
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
	// fieldRe: un campo del cuerpo JSON o el nombre de una cookie de la tabla.
	fieldRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
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
	if err := t.loadUpstreams(); err != nil {
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
	for _, name := range slices.Sorted(maps.Keys(t.Services)) {
		if _, err := config.ParsePort(t.Services[name].DefaultPort); err != nil {
			return nil, fmt.Errorf("tabla de rutas: default_port de %q: %w", name, err)
		}
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
		if r.Module == "" && !ungatedPrefixes[r.Prefix] {
			return fmt.Errorf("tabla de rutas: la ruta %q no declara modulo y quedaria sin control de acceso por modulo", r.Prefix)
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
		if p.Limit != "" && p.Limit != publicLimitWebhook {
			return fmt.Errorf("tabla de rutas: limite %q invalido en la ruta publica %q (solo %q)", p.Limit, p.Path, publicLimitWebhook)
		}
		if err := validatePublicExtras(p); err != nil {
			return err
		}
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
		if err := t.validateSelfAuthCell(s); err != nil {
			return err
		}
	}
	if t.Frontend != "" {
		if _, ok := t.Services[t.Frontend]; !ok {
			return fmt.Errorf("tabla de rutas: frontend %q no esta en services", t.Frontend)
		}
	}
	if err := t.validateWellKnown(); err != nil {
		return err
	}
	if err := t.validateAPIKeyRoutes(); err != nil {
		return err
	}
	return t.validateCellServices()
}

// loadUpstreams resuelve del entorno el destino base de cada servicio de la tabla, lo pida ya una
// ruta o no: el entorno manda y los valores del fichero son el respaldo de desarrollo. Un host o
// un puerto invalidos impiden arrancar, en vez de dar 502 en la primera peticion a ese servicio.
func (t *routeTable) loadUpstreams() error {
	t.upstreams = make(map[string]string, len(t.Services))
	for _, name := range slices.Sorted(maps.Keys(t.Services)) {
		s := t.Services[name]
		port, err := config.ParsePort(s.DefaultPort)
		if err != nil {
			return fmt.Errorf("tabla de rutas: default_port de %q: %w", name, err)
		}
		target, err := config.UpstreamURL(s.HostEnv, s.DefaultHost, port)
		if err != nil {
			return fmt.Errorf("destino del servicio %q: %w", name, err)
		}
		t.upstreams[name] = target
	}
	return nil
}

// serviceURL es la URL interna del destino base de un servicio, resuelta al cargar la tabla.
func (t *routeTable) serviceURL(name string) string {
	return t.upstreams[name]
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
// seg1 identifica el modulo; seg2 permite detectar el autoservicio (users/me, access/my-modules).
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
