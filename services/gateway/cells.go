package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/tenantcell"
	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

// Enrutado por celda.
//
// Un servicio de celda (cell_hosts_env en routes.json) se despliega una vez por celda y solo
// atiende a las empresas de la suya: TODA ruta suya se enruta por celda, y la validacion de la
// tabla impide declararle una que no se pueda. Cada instancia comprueba ademas que la empresa
// es de su celda (tenantcell.Membership): este enrutado es la primera barrera, no la unica.
//
// Rutas con sesion: la celda es la de la empresa del token, que resuelve organization
// (pkg/tenantcell). El destino base (<SERVICIO>_HOST) sirve la celda GATEWAY_BASE_CELL_CODE y
// <SERVICIO>_CELL_HOSTS las demas; una empresa de una celda sin instancia declarada, o cuya
// celda no se puede resolver, recibe 503 y no sale hacia ninguna instancia. Sin
// GATEWAY_BASE_CELL_CODE ni instancias declaradas el despliegue es de una celda y todo va al
// destino base sin preguntar a nadie.
//
// Rutas publicas: un enlace que llega por correo sin sesion lleva la celda como segmento
// {cell} de la ruta, SIN firmar: el gateway enruta por el y no verifica nada, asi que no
// recibe la clave de los enlaces. La firma del enlace incluye la celda y la comprueba el
// servicio de destino: un segmento cambiado lleva el enlace a una celda que lo rechaza.
// Cualquier segmento que no sea una celda declarada va al destino base, que lo rechaza con la
// misma respuesta que a una firma alterada: el gateway no responde nada propio y no sirve para
// averiguar que celdas existen.

const (
	cellParam = "cell"
	// baseCellEnv nombra la celda que sirven los destinos base de los servicios de celda.
	baseCellEnv = tenantcell.BaseCellEnv
	// cellDirectoryService es el servicio que sabe en que celda vive cada empresa.
	cellDirectoryService = "organization"
)

// Motivos, etiqueta reason de cell_routing_failures_total.
const (
	// routingUnresolved: organization no dio la celda.
	routingUnresolved = "unresolved"
	// routingUnknownTenant: la empresa de la sesion no existe.
	routingUnknownTenant = "unknown_tenant"
	// routingNotServed: la celda no tiene instancia declarada del servicio (error de despliegue).
	routingNotServed = "not_served"
)

// cellRoutingFailures cuenta las peticiones que no salieron hacia ninguna celda. El servicio de
// celda de destino va en cell_service: la etiqueta service la pone el recolector al gateway.
var cellRoutingFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "cell_routing_failures_total",
	Help: "Peticiones con sesion a un servicio de celda que el gateway no envio a ninguna celda.",
}, []string{"cell_service", "reason"})

func init() { prometheus.MustRegister(cellRoutingFailures) }

// routingFailuresAtZero crea a cero, al montar el enrutado, las series de un servicio para los
// motivos que puede producir: un contador que nace ya en 1 no da increase() y su primer fallo no
// avisaria (CeldaSinInstancia).
func routingFailuresAtZero(service string, reasons ...string) {
	for _, reason := range reasons {
		cellRoutingFailures.WithLabelValues(service, reason)
	}
}

// cellSegment dice si la ruta lleva {cell} como segmento entero.
func cellSegment(path string) bool {
	for _, seg := range strings.Split(path, "/") {
		if seg == "{"+cellParam+"}" {
			return true
		}
	}
	return false
}

// validateCellHostsEnv: la variable de instancias por celda tiene forma de variable, es de
// un solo servicio y no pisa el host ni el puerto de ninguno.
func (t *routeTable) validateCellHostsEnv() error {
	taken := make(map[string]bool, 2*len(t.Services)+1)
	for _, s := range t.Services {
		taken[s.HostEnv], taken[s.HostEnv+"_PORT"] = true, true
	}
	taken[baseCellEnv] = true
	seen := map[string]string{}
	for name, s := range t.Services {
		if s.CellHostsEnv == "" {
			continue
		}
		if !hostEnvRe.MatchString(s.CellHostsEnv) || taken[s.CellHostsEnv] {
			return fmt.Errorf("tabla de rutas: cell_hosts_env invalido %q en %q", s.CellHostsEnv, name)
		}
		if other, dup := seen[s.CellHostsEnv]; dup {
			return fmt.Errorf("tabla de rutas: cell_hosts_env %q repetido en %q y %q", s.CellHostsEnv, other, name)
		}
		seen[s.CellHostsEnv] = name
	}
	return nil
}

// validatePublicCell: {cell} solo como segmento entero y una vez, en toda ruta de un
// servicio de celda y solo en ellas. Una ruta de celda sin {cell} iria siempre a la celda
// por defecto, que rechaza los enlaces de las demas.
func (t *routeTable) validatePublicCell(p publicRouteSpec) error {
	byCell := cellSegment(p.Path)
	if strings.Count(p.Path, "{"+cellParam) > 1 || (!byCell && strings.Contains(p.Path, "{"+cellParam)) {
		return fmt.Errorf("tabla de rutas: en %q, {%s} va una sola vez y como segmento entero", p.Path, cellParam)
	}
	cellService := t.Services[p.Service].CellHostsEnv != ""
	switch {
	case byCell && !cellService:
		return fmt.Errorf("tabla de rutas: %q enruta por celda y el servicio %q no declara cell_hosts_env", p.Path, p.Service)
	case !byCell && cellService:
		return fmt.Errorf("tabla de rutas: %q es de un servicio de celda y no lleva {%s}", p.Path, cellParam)
	}
	return nil
}

// validateSelfAuthCell: un prefijo que autentica el propio servicio no lleva empresa verificada
// ni celda en la ruta. Solo puede ser de un servicio de celda si declara como se enruta por celda
// (selfauthcells.go): el inicio de sesion (metodo con cuerpo, ruta fija, campo del nombre de
// usuario), que ademas va con el limitador estricto porque cada intento pregunta a organization,
// y la cookie cuyo token lleva la celda. Sin eso iria siempre al destino base, que es otra celda
// para los buzones de las demas. En un servicio que no es de celda esas claves no significan nada
// y tampoco se admiten.
func (t *routeTable) validateSelfAuthCell(s selfAuthSpec) error {
	cellService := t.Services[s.Service].CellHostsEnv != ""
	declared := s.CellLogin != nil || s.CellCookie != ""
	switch {
	case !cellService && declared:
		return fmt.Errorf("tabla de rutas: el prefijo %q declara cell_login o cell_cookie y %q no es un servicio de celda", s.Prefix, s.Service)
	case !cellService:
		return nil
	case s.CellLogin == nil || s.CellCookie == "":
		return fmt.Errorf("tabla de rutas: el prefijo autenticado por el servicio %q apunta al servicio de celda %q sin cell_login y cell_cookie: el gateway no sabria a que celda llevar el inicio de sesion ni la sesion", s.Prefix, s.Service)
	}
	l := s.CellLogin
	if l.Method != "POST" && l.Method != "PUT" {
		return fmt.Errorf("tabla de rutas: cell_login de %q con metodo %q: el nombre de usuario viaja en el cuerpo (POST o PUT)", s.Prefix, l.Method)
	}
	if !strings.HasPrefix(l.Path, "/") || strings.Contains(l.Path, "..") || strings.ContainsAny(l.Path, "{}*") {
		return fmt.Errorf("tabla de rutas: ruta invalida %q en cell_login de %q", l.Path, s.Prefix)
	}
	if !fieldRe.MatchString(l.UsernameField) || !fieldRe.MatchString(s.CellCookie) {
		return fmt.Errorf("tabla de rutas: username_field %q o cell_cookie %q invalidos en %q", l.UsernameField, s.CellCookie, s.Prefix)
	}
	for _, strict := range s.StrictLimit {
		if strict.Method == l.Method && strict.Path == l.Path {
			return nil
		}
	}
	return fmt.Errorf("tabla de rutas: el inicio de sesion por celda %s %s de %q debe ir tambien en strict_limit: cada intento pregunta la celda a organization", l.Method, l.Path, s.Prefix)
}

// validateCellServices: un servicio de celda tiene alguna ruta, y todas se pueden enrutar por
// celda (con sesion, por la empresa; publicas, por {cell}; autenticadas por el servicio, por el
// dominio del buzon y la cookie, validateSelfAuthCell). El frontend no lleva ni empresa
// verificada ni celda en la ruta: iria siempre al destino base, que es otra celda para las
// empresas de las demas.
func (t *routeTable) validateCellServices() error {
	routed := map[string]bool{}
	for _, r := range t.Routes {
		routed[r.Service] = true
	}
	for _, p := range t.Public {
		if cellSegment(p.Path) {
			routed[p.Service] = true
		}
	}
	for _, s := range t.SelfAuthenticated {
		if s.CellLogin != nil && t.Services[s.Service].CellHostsEnv != "" {
			routed[s.Service] = true
		}
	}
	if t.Frontend != "" && t.Services[t.Frontend].CellHostsEnv != "" {
		return fmt.Errorf("tabla de rutas: el frontend %q no puede ser un servicio de celda", t.Frontend)
	}
	if _, ok := t.Services[cellDirectoryService]; !ok {
		for name, s := range t.Services {
			if s.CellHostsEnv != "" {
				return fmt.Errorf("tabla de rutas: el servicio de celda %q necesita %q en services para resolver la celda de cada empresa", name, cellDirectoryService)
			}
		}
	}
	for name, s := range t.Services {
		if s.CellHostsEnv != "" && !routed[name] {
			return fmt.Errorf("tabla de rutas: %q declara cell_hosts_env sin ninguna ruta con sesion, publica con {%s} ni autenticada por el servicio con cell_login", name, cellParam)
		}
	}
	return nil
}

// loadCellTargets lee del entorno las instancias por celda de cada servicio de celda y la
// celda de los destinos base, con las reglas de tenantcell.LoadInstances (las mismas que
// domain-service): una entrada mal formada, instancias sin celda base o la celda base declarada
// tambien como instancia impiden arrancar.
func (t *routeTable) loadCellTargets() error {
	serviceOf := map[string]string{}
	envs := make([]string, 0, len(t.Services))
	for name, s := range t.Services {
		if s.CellHostsEnv != "" {
			serviceOf[s.CellHostsEnv] = name
			envs = append(envs, s.CellHostsEnv)
		}
	}
	inst, err := tenantcell.LoadInstances(os.Getenv, baseCellEnv, envs...)
	if err != nil {
		return err
	}
	t.cellTargets = make(map[string]map[string]string, len(serviceOf))
	for env, name := range serviceOf {
		t.cellTargets[name] = inst.ByEnv[env]
	}
	t.baseCell = inst.BaseCell
	return nil
}

// cellCodes devuelve, por servicio de celda, las celdas con instancia propia, ordenadas.
func (t *routeTable) cellCodes() map[string][]string {
	out := make(map[string][]string, len(t.cellTargets))
	for name, targets := range t.cellTargets {
		codes := make([]string, 0, len(targets))
		for code := range targets {
			codes = append(codes, code)
		}
		sort.Strings(codes)
		out[name] = codes
	}
	return out
}

// cellCoverageGaps devuelve, por servicio de celda, las celdas que otro servicio de celda
// declara y el no. No impide arrancar (una celda puede abrirse servicio a servicio), pero sus
// empresas reciben 503 en ese servicio hasta que se declare.
func (t *routeTable) cellCoverageGaps() map[string][]string {
	all := map[string]bool{}
	for _, targets := range t.cellTargets {
		for code := range targets {
			all[code] = true
		}
	}
	gaps := map[string][]string{}
	for name, targets := range t.cellTargets {
		for code := range all {
			if _, ok := targets[code]; !ok {
				gaps[name] = append(gaps[name], code)
			}
		}
		sort.Strings(gaps[name])
	}
	for name, missing := range gaps {
		if len(missing) == 0 {
			delete(gaps, name)
		}
	}
	return gaps
}

// cellRouter elige la instancia por el segmento {cell}; lo que no es una celda declarada va
// al destino base.
type cellRouter struct {
	byCell   map[string]http.Handler
	fallback http.Handler
}

func (c cellRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h, ok := c.byCell[chi.URLParam(r, cellParam)]; ok {
		h.ServeHTTP(w, r)
		return
	}
	c.fallback.ServeHTTP(w, r)
}

// mountPublic monta las rutas publicas de la tabla: las que llevan {cell}, por celda; las
// demas, al destino base de su servicio. Un proxy por destino.
func mountPublic(r chi.Router, t *routeTable, internalToken string, webhookLimit func(http.Handler) http.Handler) {
	proxies := map[string]http.Handler{}
	proxyFor := func(target string) http.Handler {
		if p, ok := proxies[target]; ok {
			return p
		}
		p := reverseProxy(target, internalToken)
		proxies[target] = p
		return p
	}
	routers := map[string]http.Handler{}
	for _, p := range t.Public {
		h := proxyFor(t.serviceURL(p.Service))
		if cellSegment(p.Path) {
			router, ok := routers[p.Service]
			if !ok {
				byCell := make(map[string]http.Handler, len(t.cellTargets[p.Service]))
				for code, target := range t.cellTargets[p.Service] {
					byCell[code] = proxyFor(target)
				}
				router = cellRouter{byCell: byCell, fallback: h}
				routers[p.Service] = router
			}
			h = router
		}
		if p.Limit == publicLimitWebhook {
			h = webhookLimit(h)
		}
		r.Method(p.Method, p.Path, h)
	}
}

// sessionHandlers devuelve, por servicio, el manejador final de sus rutas con sesion: su
// destino base o, para un servicio de celda con el enrutado por celda activo
// (GATEWAY_BASE_CELL_CODE), el que elige la instancia por la celda de la empresa. cells es
// nil exactamente cuando no hay celda base.
func sessionHandlers(t *routeTable, internalToken string, cells *tenantcell.Resolver, logger *zap.Logger) map[string]http.Handler {
	routed := make(map[string]bool, len(t.Routes))
	for _, rt := range t.Routes {
		routed[rt.Service] = true
	}
	out := make(map[string]http.Handler, len(t.Services))
	for name, s := range t.Services {
		base := reverseProxy(t.serviceURL(name), internalToken)
		switch {
		case s.CellHostsEnv == "":
			out[name] = base
		case cells == nil:
			out[name] = varyByTargetCell(base)
		default:
			byCell := map[string]http.Handler{t.baseCell: base}
			for code, target := range t.cellTargets[name] {
				byCell[code] = reverseProxy(target, internalToken)
			}
			out[name] = varyByTargetCell(tenantCellRouter{service: name, byCell: byCell, cells: cells, logger: logger})
			if routed[name] {
				routingFailuresAtZero(name, routingUnknownTenant, routingUnresolved, routingNotServed)
			}
		}
	}
	return out
}

// tenantCellRouter lleva una peticion con sesion a la instancia de la celda de su empresa, o a
// la celda destino que targetCellGate valido para un operador (target.go). Va al final de la
// cadena, despues de la sesion y del RBAC: una peticion denegada no llega a preguntar la celda.
type tenantCellRouter struct {
	service string
	byCell  map[string]http.Handler
	cells   *tenantcell.Resolver
	logger  *zap.Logger
}

func (c tenantCellRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if cell, ok := routedTargetFrom(r.Context()); ok {
		h, served := c.byCell[cell]
		if !served {
			c.refuse(w, routingNotServed, middleware.GetTenantID(r.Context()), cell)
			response.Err(w, http.StatusServiceUnavailable, tenantcell.CodeCellUnavailable, "el servicio no esta disponible en la celda destino")
			return
		}
		r.Header.Set(middleware.HeaderOperatorCell, cell)
		h.ServeHTTP(w, r)
		return
	}
	tenantID := middleware.GetTenantID(r.Context())
	cell, err := c.cells.CellOf(r.Context(), tenantID)
	if err != nil {
		if errors.Is(err, tenantcell.ErrUnknownTenant) {
			c.refuse(w, routingUnknownTenant, tenantID, "")
			response.Err(w, http.StatusForbidden, "FORBIDDEN", "la sesion no corresponde a ninguna empresa")
			return
		}
		c.refuse(w, routingUnresolved, tenantID, "")
		response.Err(w, http.StatusServiceUnavailable, "CELL_UNAVAILABLE", "no se pudo determinar la celda de tu empresa; intentalo de nuevo")
		return
	}
	h, ok := c.byCell[cell]
	if !ok {
		c.refuse(w, routingNotServed, tenantID, cell)
		response.Err(w, http.StatusServiceUnavailable, "CELL_UNAVAILABLE", "el servicio no esta disponible para la celda de tu empresa")
		return
	}
	h.ServeHTTP(w, r)
}

func (c tenantCellRouter) refuse(w http.ResponseWriter, reason, tenantID, cell string) {
	cellRoutingFailures.WithLabelValues(c.service, reason).Inc()
	w.Header().Set("Cache-Control", "no-store")
	c.logger.Warn("celdas: peticion no enviada a ninguna celda",
		zap.String("service", c.service), zap.String("reason", reason),
		zap.String("tenant_id", tenantID), zap.String("cell", cell))
}

// exceptWebhooks aplica limit a todo /api/v1 menos a las rutas publicas marcadas como webhook,
// que llevan su propio cupo en mountPublic. Se decide antes de enrutar, con un enrutador que
// solo conoce esas rutas: la plantilla ({tenantID}) casa igual que en el enrutador real.
func exceptWebhooks(t *routeTable, limit func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	webhooks := chi.NewRouter()
	n := 0
	for _, p := range t.Public {
		if p.Limit == publicLimitWebhook {
			webhooks.Method(p.Method, "/api/v1"+p.Path, http.NotFoundHandler())
			n++
		}
	}
	if n == 0 {
		return limit
	}
	return func(next http.Handler) http.Handler {
		limited := limit(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if webhooks.Match(chi.NewRouteContext(), r.Method, r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			limited.ServeHTTP(w, r)
		})
	}
}
