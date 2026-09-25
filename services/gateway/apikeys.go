package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/apikey"
	"github.com/alonsosss/corforce-email/pkg/config"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

// Claves de API de empresa en el gateway (docs/adr/0013-claves-de-api-y-relay-smtp.md). Un bearer
// cfm_... solo se acepta en las rutas de api_key_routes; en cualquier otra se rechaza sin
// probarlo como JWT. La clave se resuelve contra access-control (pkg/apikey, cache corta que
// respeta la marca de revocacion) y la peticion sigue como la de una sesion: la empresa y el
// alcance efectivo viajan al servicio, sin usuario ni roles, y el RBAC la gatea por su alcance.

const (
	apiKeyLimiterName          = "gateway:api-key"
	defaultAPIKeyRatePerMin    = 600
	maxAPIKeyRatePerMin        = 60000
	defaultAPIKeyCacheTTL      = apikey.DefaultCacheTTL
	apiKeyResultOK             = "ok"
	apiKeyResultInvalid        = "invalid"
	apiKeyResultUnavailable    = "unavailable"
	apiKeyResultRouteForbidden = "route_not_allowed"
	apiKeyResultRateLimited    = "rate_limited"
)

// apiKeyRequests permite ver el uso y el abuso de las claves sin datos de la empresa: un pico de
// invalid es alguien probando claves, uno de route_not_allowed una integracion mal configurada.
var apiKeyRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "gateway_api_key_requests_total",
	Help: "Peticiones autenticadas con clave de API por resultado.",
}, []string{"result"})

func init() { prometheus.MustRegister(apiKeyRequests) }

var apiKeyMethods = map[string]bool{
	http.MethodGet: true, http.MethodPost: true, http.MethodPut: true, http.MethodPatch: true, http.MethodDelete: true,
}

// validateAPIKeyRoutes: cada ruta cuelga de un prefijo con modulo (sin modulo el RBAC no podria
// acotarla), no lleva comodines y no se repite. La tabla que no cumple no arranca.
func (t *routeTable) validateAPIKeyRoutes() error {
	gated := make(map[string]bool, len(t.Routes))
	for _, r := range t.Routes {
		if r.Module != "" {
			gated[r.Prefix] = true
		}
	}
	seen := make(map[string]bool, len(t.APIKeyRoutes)+len(t.ProvisioningRoutes))
	for _, rt := range append(append([]methodPathSpec(nil), t.APIKeyRoutes...), t.ProvisioningRoutes...) {
		if !apiKeyMethods[rt.Method] {
			return fmt.Errorf("tabla de rutas: metodo %q invalido en api_key_routes", rt.Method)
		}
		if !strings.HasPrefix(rt.Path, "/") || strings.ContainsAny(rt.Path, "*?#") || strings.Contains(rt.Path, "..") || strings.Contains(rt.Path, "//") {
			return fmt.Errorf("tabla de rutas: ruta %q invalida en api_key_routes", rt.Path)
		}
		seg, _ := pathSegments("/api/v1" + rt.Path)
		if !gated[seg] {
			return fmt.Errorf("tabla de rutas: la ruta de clave de API %q no cuelga de un prefijo con modulo", rt.Path)
		}
		key := rt.Method + " " + rt.Path
		if seen[key] {
			// Repetida dentro de una lista, o en las dos: lo segundo daria a las dos familias la
			// misma ruta, que es justo lo que las listas separadas evitan.
			return fmt.Errorf("tabla de rutas: ruta de clave de API repetida %s", key)
		}
		seen[key] = true
	}
	envio, err := t.matcherFor(t.APIKeyRoutes)
	if err != nil {
		return err
	}
	aprov, err := t.matcherFor(t.ProvisioningRoutes)
	if err != nil {
		return err
	}
	// Disjuntas de verdad, no solo sin repetir el texto: dos patrones distintos pueden casar la
	// misma URL ("/x/{id}" y "/x/pendientes"), y entonces esa URL la alcanzarian LAS DOS familias,
	// que es justo lo que estas listas separadas evitan. Se prueba cada ruta contra el otro router
	// con los parametros sustituidos por un valor cualquiera.
	for _, par := range []struct {
		rutas []methodPathSpec
		otro  *chi.Mux
		lista string
	}{{t.APIKeyRoutes, aprov, "api_key_routes"}, {t.ProvisioningRoutes, envio, "provisioning_routes"}} {
		for _, rt := range par.rutas {
			if par.otro.Match(chi.NewRouteContext(), rt.Method, sinParametros(rt.Path)) {
				return fmt.Errorf("tabla de rutas: %s %q de %s la alcanza tambien la otra familia de credencial",
					rt.Method, rt.Path, par.lista)
			}
		}
	}
	return nil
}

// parametroDeRuta casa {parametro} en un patron de chi.
var parametroDeRuta = regexp.MustCompile(`\{[^}]*\}`)

// sinParametros sustituye cada {parametro} por un valor cualquiera, para poder preguntarle a un
// router si casaria esa URL.
func sinParametros(patron string) string { return parametroDeRuta.ReplaceAllString(patron, "x") }

// matcherFor arma el router que reconoce una lista de rutas. chi entra en panico con un patron mal
// escrito: se convierte en error de arranque.
func (t *routeTable) matcherFor(routes []methodPathSpec) (m *chi.Mux, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			m, err = nil, fmt.Errorf("tabla de rutas: rutas de credencial: %v", rec)
		}
	}()
	m = chi.NewRouter()
	noop := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	for _, rt := range routes {
		m.Method(rt.Method, rt.Path, noop)
	}
	return m, nil
}

// apiKeyResolver es lo que el gateway pide a pkg/apikey.
type apiKeyResolver interface {
	Resolve(ctx context.Context, token, clientIP string) (*apikey.Principal, error)
}

type apiKeyGate struct {
	// routes por familia: la de envio y la de aprovisionamiento no comparten ninguna ruta.
	routes   map[string]*chi.Mux
	resolver apiKeyResolver
	limiter  *middleware.RateLimiter
	logger   *zap.Logger
}

// apiKeySettings son el cupo por clave y la vida de la cache, leidos al arrancar.
type apiKeySettings struct {
	ratePerMin int
	cacheTTL   time.Duration
}

func loadAPIKeySettings() (apiKeySettings, error) {
	var s apiKeySettings
	var err error
	if s.ratePerMin, err = config.EnvInt("API_KEY_RATE_LIMIT_PER_MIN", defaultAPIKeyRatePerMin, 1, maxAPIKeyRatePerMin); err != nil {
		return s, err
	}
	if s.cacheTTL, err = config.EnvDuration("API_KEY_CACHE_TTL", defaultAPIKeyCacheTTL, time.Second, apikey.MaxCacheTTL); err != nil {
		return s, err
	}
	return s, nil
}

func newAPIKeyGate(t *routeTable, resolver apiKeyResolver, limiter *middleware.RateLimiter, logger *zap.Logger) (*apiKeyGate, error) {
	envio, err := t.matcherFor(t.APIKeyRoutes)
	if err != nil {
		return nil, err
	}
	aprov, err := t.matcherFor(t.ProvisioningRoutes)
	if err != nil {
		return nil, err
	}
	routes := map[string]*chi.Mux{apikey.KindSending: envio, apikey.KindProvisioning: aprov}
	return &apiKeyGate{routes: routes, resolver: resolver, limiter: limiter, logger: logger}, nil
}

// bearerToken devuelve el token del Authorization: Bearer, o vacio.
func bearerToken(r *http.Request) string {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}

// allowedRoute: cada familia solo entra en SU lista. Una credencial de envio en una ruta de
// aprovisionamiento se rechaza igual que en cualquier otra ruta no listada, y al reves.
func (g *apiKeyGate) allowedRoute(r *http.Request, kind string) bool {
	path, ok := strings.CutPrefix(r.URL.Path, "/api/v1")
	if !ok {
		return false
	}
	m := g.routes[kind]
	if m == nil {
		return false
	}
	return m.Match(chi.NewRouteContext(), r.Method, path)
}

// authenticate envuelve la autenticacion por JWT: un bearer que no es una clave sigue por ella.
func (g *apiKeyGate) authenticate(jwt func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		byJWT := jwt(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := bearerToken(r)
			kind := apikey.KindOf(token)
			if kind == "" {
				byJWT.ServeHTTP(w, r)
				return
			}
			if !g.allowedRoute(r, kind) {
				apiKeyRequests.WithLabelValues(apiKeyResultRouteForbidden).Inc()
				apiKeyError(w, http.StatusUnauthorized, "API_KEY_ROUTE_NOT_ALLOWED", "esta ruta no admite claves de API")
				return
			}
			p, err := g.resolver.Resolve(r.Context(), token, middleware.GetClientIP(r.Context()))
			switch {
			case errors.Is(err, apikey.ErrInvalid):
				apiKeyRequests.WithLabelValues(apiKeyResultInvalid).Inc()
				apiKeyError(w, http.StatusUnauthorized, "API_KEY_INVALID", "clave de API inválida, revocada o caducada")
				return
			case err != nil:
				apiKeyRequests.WithLabelValues(apiKeyResultUnavailable).Inc()
				g.logger.Warn("gateway: no se pudo comprobar una clave de API", zap.Error(err))
				apiKeyError(w, http.StatusServiceUnavailable, "API_KEY_UNAVAILABLE", "no se pudo comprobar la clave de API")
				return
			}
			if ok, reset := g.limiter.AllowKey(r.Context(), p.ID); !ok {
				apiKeyRequests.WithLabelValues(apiKeyResultRateLimited).Inc()
				w.Header().Set("Retry-After", strconv.Itoa(max(1, int(reset.Seconds()+0.999))))
				apiKeyError(w, http.StatusTooManyRequests, "RATE_LIMITED", "la clave superó su cupo de peticiones")
				return
			}
			// La familia que dice el prefijo tiene que ser la de la credencial guardada. Lo
			// comprueba tambien access-control; aqui se repite porque es lo que separa los poderes
			// y no puede depender de una sola capa. Sin "!= vacia": pkg/apikey normaliza la
			// respuesta antigua a la familia de envio, asi que un vacio aqui seria una respuesta
			// que no entendemos y no debe pasar por ninguna familia.
			if p.Kind != kind {
				apiKeyRequests.WithLabelValues(apiKeyResultInvalid).Inc()
				apiKeyError(w, http.StatusUnauthorized, "API_KEY_INVALID", "clave de API inválida, revocada o caducada")
				return
			}
			apiKeyRequests.WithLabelValues(apiKeyResultOK).Inc()
			ctx := middleware.WithAPIKey(middleware.WithTenantID(r.Context(), p.TenantID), p.ID, p.Scopes)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func apiKeyError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `{"error":{"code":%q,"message":%q}}`, code, message)
}

// allowAPIKey es el RBAC de una peticion de clave: la lectura exige algun permiso de la clave en
// el modulo de la ruta; la escritura, la accion del metodo (DELETE delete, PUT y PATCH update) o,
// en POST, algun permiso que no sea de lectura. Se aplica en cualquier modo del RBAC: una clave
// nunca pasa sin alcance.
func (e *rbacEnforcer) allowAPIKey(w http.ResponseWriter, r *http.Request) bool {
	seg1, _ := pathSegments(r.URL.Path)
	module := e.modules[seg1]
	scopes := middleware.GetAPIKeyScopes(r.Context())
	read := !rbacWriteMethods[r.Method] || e.isReadPost(r)
	action := "read"
	if !read {
		action = requiredAction(r.Method)
	}
	allowed := false
	for _, s := range scopes {
		if module == "" || s.Module != module {
			continue
		}
		switch {
		case read:
			allowed = true
		case action == "":
			allowed = allowed || (s.Action != "read" && s.Action != "export")
		default:
			allowed = allowed || s.Action == action
		}
	}
	if allowed {
		return true
	}
	rbacDenials.WithLabelValues(module, action, "true").Inc()
	e.logger.Info("rbac: clave de API sin alcance para esta ruta",
		zap.String("api_key_id", middleware.GetAPIKeyID(r.Context())), zap.String("module", module),
		zap.String("method", r.Method), zap.String("path", r.URL.Path))
	e.deny(w)
	return false
}
