package tenantcell

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

// Codigos de respuesta estables de Membership.
const (
	CodeNotInCell       = "TENANT_NOT_IN_CELL"
	CodeCellUnavailable = "CELL_UNAVAILABLE"
)

// Motivos de rechazo, etiqueta reason de cell_membership_refusals_total.
const (
	reasonForeign    = "foreign_tenant"
	reasonUnknown    = "unknown_tenant"
	reasonUnresolved = "unresolved"
)

// refusals nace con sus tres motivos a cero: un contador que aparece ya en 1 no da increase()
// y la alerta EmpresaEnCeldaAjena no veria el primer rechazo.
var refusals = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "cell_membership_refusals_total",
	Help: "Peticiones por una empresa que un servicio de celda rechazo sin atenderlas porque la empresa no es de su celda o no se pudo comprobar.",
}, []string{"reason"})

func init() {
	prometheus.MustRegister(refusals)
	for _, reason := range []string{reasonForeign, reasonUnknown, reasonUnresolved} {
		refusals.WithLabelValues(reason)
	}
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
type Membership struct {
	cell     string
	resolver *Resolver
	logger   *zap.Logger
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
	orgURL, err := organizationURL(os.Getenv("ORGANIZATION_URL"))
	if err != nil {
		return nil, err
	}
	token, err := middleware.InternalGatewayToken()
	if err != nil {
		return nil, err
	}
	return NewMembership(strings.TrimSpace(os.Getenv("CELL_CODE")), NewResolver(orgURL, token, logger), logger)
}

func organizationURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if raw == "" || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("ORGANIZATION_URL %q: se espera la URL interna de organization (http o https, sin ruta de consulta)", raw)
	}
	return raw, nil
}

// Cell es la celda de esta instancia.
func (m *Membership) Cell() string { return m.cell }

// Require rechaza, antes de cualquier ruta, la peticion de una empresa que no es de la celda:
// 403 TENANT_NOT_IN_CELL si organization la situa en otra celda o no la conoce, 503
// CELL_UNAVAILABLE si no hay respuesta aplicable. Ninguna de las dos llega a un handler. Va
// despues de RequireGatewayToken e InjectFromGateway.
func (m *Membership) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

func (m *Membership) refuse(w http.ResponseWriter, r *http.Request, reason, tenantID, cell string) {
	refusals.WithLabelValues(reason).Inc()
	w.Header().Set("Cache-Control", "no-store")
	m.logger.Warn("celdas: peticion de una empresa que no es de esta celda",
		zap.String("reason", reason), zap.String("tenant_id", tenantID), zap.String("tenant_cell", cell),
		zap.String("cell", m.cell), zap.String("method", r.Method), zap.String("path", r.URL.Path))
}
