package tenantcell

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

// Codigos de respuesta estables de Membership.
const (
	CodeNotInCell       = "TENANT_NOT_IN_CELL"
	CodeCellUnavailable = "CELL_UNAVAILABLE"
	// CodeTargetCellMismatch: la celda destino del operador no es la de esta instancia.
	CodeTargetCellMismatch = "TARGET_CELL_MISMATCH"
	// CodePlatformScopeOnly: con celda destino solo se atienden rutas de plataforma, y solo al
	// superadmin.
	CodePlatformScopeOnly = "PLATFORM_SCOPE_ONLY"
)

// Motivos de rechazo, etiqueta reason de cell_membership_refusals_total.
const (
	reasonForeign    = "foreign_tenant"
	reasonUnknown    = "unknown_tenant"
	reasonUnresolved = "unresolved"
	// Peticiones de operador con celda destino (HeaderOperatorCell).
	reasonOperatorWrongCell   = "operator_wrong_cell"
	reasonOperatorNotPlatform = "operator_not_platform"
	reasonOperatorTenantRoute = "operator_tenant_route"
)

// refusals nace con sus motivos a cero: un contador que aparece ya en 1 no da increase() y la
// alerta EmpresaEnCeldaAjena no veria el primer rechazo.
var refusals = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "cell_membership_refusals_total",
	Help: "Peticiones que un servicio de celda rechazo sin atenderlas: una empresa que no es de su celda o no se pudo comprobar, o un operador con celda destino fuera de las rutas de plataforma.",
}, []string{"reason"})

func init() {
	prometheus.MustRegister(refusals)
	for _, reason := range []string{reasonForeign, reasonUnknown, reasonUnresolved,
		reasonOperatorWrongCell, reasonOperatorNotPlatform, reasonOperatorTenantRoute} {
		refusals.WithLabelValues(reason)
	}
}

// Route es una ruta de plataforma de un servicio de celda: metodo y patron completo de chi, tal
// como los monta el servicio ("/api/v1/mail-security/firewall/networks/{id}").
type Route struct {
	Method, Pattern string
}

// Membership es la segunda barrera de un servicio de celda, detras del enrutado del gateway:
// una peticion que actua por una empresa solo pasa si organization dice que esa empresa vive en
// la celda de esta instancia. Sin ella, un gateway sin GATEWAY_BASE_CELL_CODE o con las
// instancias cruzadas llevaria a una empresa a otra celda, y RLS aislaria sus lecturas pero no
// sus escrituras, que quedarian en la base de una celda que no es la suya.
//
// Actua por una empresa toda peticion que trae X-Tenant-ID (la de una persona, por el gateway,
// y la de un servicio, como domain-service) o que trae usuario. Las demas (servicio a servicio
// sin empresa, enlaces publicos firmados con la celda) pasan: no hay empresa que comprobar.
//
// Operadores de la plataforma: el superadmin es de la empresa de plataforma, que vive en una
// celda; para operar otra, el gateway le enruta con una celda destino explicita y la reenvia en
// HeaderOperatorCell. Esa peticion no se juzga por su empresa, sino por otra regla mas
// estrecha: la celda destino es la de esta instancia, quien llama tiene el rol superadmin y la
// ruta es una de las rutas de plataforma declaradas con AcceptOperators. Una ruta de datos de
// empresa nunca se atiende con celda destino, ni siquiera en la celda del propio operador: el
// superadmin no alcanza por aqui los datos de ninguna empresa. Sin AcceptOperators, toda
// peticion con celda destino se rechaza.
type Membership struct {
	cell     string
	resolver *Resolver
	logger   *zap.Logger

	operatorRoutes chi.Routes
	platform       map[Route]bool
}

// NewMembership cierra el servicio a las empresas que no son de la celda cell.
func NewMembership(cell string, resolver *Resolver, logger *zap.Logger) (*Membership, error) {
	if !ValidCode(cell) {
		return nil, fmt.Errorf("CELL_CODE %q no es un codigo de celda (minusculas, digitos y guiones)", cell)
	}
	if resolver == nil {
		return nil, errors.New("sin resolvedor de celdas")
	}
	return &Membership{cell: cell, resolver: resolver, logger: logger}, nil
}

// MembershipFromEnv lee CELL_CODE, ORGANIZATION_URL e INTERNAL_GATEWAY_TOKEN. Cualquiera que
// falte o este mal formada es un error: un servicio de celda no arranca sin su segunda barrera.
func MembershipFromEnv(logger *zap.Logger) (*Membership, error) {
	orgURL, err := OrganizationURLFromEnv()
	if err != nil {
		return nil, err
	}
	token, err := middleware.InternalGatewayToken()
	if err != nil {
		return nil, err
	}
	return NewMembership(strings.TrimSpace(os.Getenv("CELL_CODE")), NewResolver(orgURL, token, logger), logger)
}

// Cell es la celda de esta instancia.
func (m *Membership) Cell() string { return m.cell }

// AcceptOperators declara las rutas de plataforma del servicio: las unicas que atienden a un
// operador con celda destino. routes es el router que el servicio monta detras de Require, y
// cada ruta declarada debe existir en el con ese metodo y patron exactos, o no arranca: una
// declaracion que no casa con nada abriria o cerraria rutas distintas de las que dice. Cada
// ruta declarada exige ademas en su handler un permiso de alcance plataforma.
func (m *Membership) AcceptOperators(routes chi.Routes, platform []Route) error {
	if routes == nil || len(platform) == 0 {
		return errors.New("rutas de plataforma: se necesita el router y al menos una ruta")
	}
	mounted := map[Route]bool{}
	if err := chi.Walk(routes, func(method, pattern string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		mounted[Route{Method: method, Pattern: pattern}] = true
		return nil
	}); err != nil {
		return fmt.Errorf("rutas de plataforma: %w", err)
	}
	declared := make(map[Route]bool, len(platform))
	for _, rt := range platform {
		if !mounted[rt] {
			return fmt.Errorf("rutas de plataforma: %s %s no esta montada en el servicio", rt.Method, rt.Pattern)
		}
		declared[rt] = true
	}
	m.operatorRoutes, m.platform = routes, declared
	return nil
}

// Require rechaza, antes de cualquier ruta, la peticion de una empresa que no es de la celda:
// 403 TENANT_NOT_IN_CELL si organization la situa en otra celda o no la conoce, 503
// CELL_UNAVAILABLE si no hay respuesta aplicable. Ninguna de las dos llega a un handler. Una
// peticion con celda destino sigue la regla del operador (requireOperator). Va despues de
// RequireGatewayToken e InjectFromGateway.
func (m *Membership) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if targets, ok := r.Header[http.CanonicalHeaderKey(middleware.HeaderOperatorCell)]; ok {
			m.requireOperator(w, r, next, targets)
			return
		}
		tenantID := middleware.GetTenantID(r.Context())
		if tenantID == "" && middleware.GetUserID(r.Context()) == "" {
			next.ServeHTTP(w, r)
			return
		}
		cell, err := m.resolver.CellOf(r.Context(), tenantID)
		switch {
		case err == nil && cell == m.cell:
			next.ServeHTTP(w, r)
		case err == nil:
			m.refuse(w, r, reasonForeign, tenantID, cell)
			response.Err(w, http.StatusForbidden, CodeNotInCell, "la empresa de la peticion no pertenece a esta celda")
		case errors.Is(err, ErrUnknownTenant):
			m.refuse(w, r, reasonUnknown, tenantID, "")
			response.Err(w, http.StatusForbidden, CodeNotInCell, "la empresa de la peticion no pertenece a esta celda")
		default:
			m.refuse(w, r, reasonUnresolved, tenantID, "")
			response.Err(w, http.StatusServiceUnavailable, CodeCellUnavailable, "no se pudo comprobar la celda de tu empresa; intentalo de nuevo")
		}
	})
}

// requireOperator atiende una peticion con celda destino solo si la celda es esta, quien llama
// es superadmin y la ruta es de plataforma. No pregunta a organization: la empresa del operador
// no es de esta celda y no es la que se opera.
func (m *Membership) requireOperator(w http.ResponseWriter, r *http.Request, next http.Handler, targets []string) {
	tenantID := middleware.GetTenantID(r.Context())
	switch {
	case len(targets) != 1 || targets[0] != m.cell:
		m.refuse(w, r, reasonOperatorWrongCell, tenantID, strings.Join(targets, ","))
		response.Err(w, http.StatusForbidden, CodeTargetCellMismatch, "la celda destino de la peticion no es esta celda")
	case !platformOperator(r):
		m.refuse(w, r, reasonOperatorNotPlatform, tenantID, targets[0])
		response.Err(w, http.StatusForbidden, CodePlatformScopeOnly, "con celda destino solo se atiende a un operador de la plataforma")
	case !m.platformRoute(r):
		m.refuse(w, r, reasonOperatorTenantRoute, tenantID, targets[0])
		response.Err(w, http.StatusForbidden, CodePlatformScopeOnly, "con celda destino solo se atienden las rutas de plataforma")
	default:
		m.logger.Info("celdas: operador de la plataforma con celda destino",
			zap.String("user_id", middleware.GetUserID(r.Context())), zap.String("cell", m.cell),
			zap.String("method", r.Method), zap.String("path", r.URL.Path))
		next.ServeHTTP(w, r)
	}
}

// platformOperator: una persona con el rol superadmin. Los roles llegan del gateway
// (InjectFromGateway), que es el unico que puede escribirlos.
func platformOperator(r *http.Request) bool {
	if middleware.GetUserID(r.Context()) == "" {
		return false
	}
	for _, role := range middleware.GetRoles(r.Context()) {
		if role == middleware.RoleSuperadmin {
			return true
		}
	}
	return false
}

// platformRoute resuelve la ruta con el mismo router y la misma ruta que usara chi al
// atenderla, asi que decide sobre el handler que de verdad correria.
func (m *Membership) platformRoute(r *http.Request) bool {
	if m.operatorRoutes == nil {
		return false
	}
	path := r.URL.RawPath
	if path == "" {
		path = r.URL.Path
	}
	pattern := m.operatorRoutes.Find(chi.NewRouteContext(), r.Method, path)
	return pattern != "" && m.platform[Route{Method: r.Method, Pattern: pattern}]
}

func (m *Membership) refuse(w http.ResponseWriter, r *http.Request, reason, tenantID, cell string) {
	refusals.WithLabelValues(reason).Inc()
	w.Header().Set("Cache-Control", "no-store")
	m.logger.Warn("celdas: peticion de una empresa que no es de esta celda",
		zap.String("reason", reason), zap.String("tenant_id", tenantID), zap.String("tenant_cell", cell),
		zap.String("cell", m.cell), zap.String("method", r.Method), zap.String("path", r.URL.Path))
}
