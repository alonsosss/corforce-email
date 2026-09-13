package main

import (
	"context"
	"net/http"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/tenantcell"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

// Celda destino explicita de un operador de la plataforma.
//
// Una peticion con sesion a un servicio de celda va a la celda de la empresa del token. El
// superadmin es de la empresa de plataforma, que vive en una celda: sin mas, solo opera esa.
// Para operar otra (el cortafuegos de pe-02, por ejemplo) nombra la celda en la cabecera
// X-Target-Cell. Cabecera y no ruta ni consulta: no cambia la forma de ninguna ruta de ningun
// servicio ni se mezcla con sus parametros, no queda en la URL (historial, Referer, registros de
// acceso) y un origen ajeno no puede anadirla sin preflight CORS; la sesion ademas va en
// Authorization, que el navegador no manda solo.
//
// Reglas, todas antes de salir hacia ninguna instancia y sin volver nunca a la celda de la
// empresa:
//   - Solo con el rol superadmin del token verificado. Cualquier otra sesion que la mande recibe
//     403 TARGET_CELL_FORBIDDEN: no se ignora, porque quien la manda espera operar otra celda.
//   - Un solo valor con forma de codigo de celda: si no, 400 INVALID_TARGET_CELL.
//   - Solo en rutas de un servicio de celda: si no, 400 TARGET_CELL_NOT_APPLICABLE.
//   - Solo una celda con instancia declarada de ese servicio (la base o <SERVICIO>_CELL_HOSTS):
//     si no, o si el despliegue no enruta por celda, 503 CELL_UNAVAILABLE.
//
// La instancia recibe la celda en X-Operator-Cell (middleware.HeaderOperatorCell, que el
// gateway borra de toda peticion de cliente) y solo atiende con ella sus rutas de plataforma y
// al superadmin (tenantcell.Membership). Toda peticion que la pida, atendida o rechazada, queda
// en el rastro de auditoria con la celda pedida.

const targetCellHeader = "X-Target-Cell"

// Codigos de respuesta estables de la celda destino.
const (
	codeTargetCellForbidden     = "TARGET_CELL_FORBIDDEN"
	codeInvalidTargetCell       = "INVALID_TARGET_CELL"
	codeTargetCellNotApplicable = "TARGET_CELL_NOT_APPLICABLE"
)

// Motivos de rechazo, etiqueta reason de cell_target_refusals_total.
const (
	targetNotOperator    = "not_operator"
	targetMalformed      = "malformed"
	targetNotCellService = "not_cell_service"
	targetNotServed      = "not_served"
)

// invalidTargetAudit sustituye en el rastro a un valor que no es un codigo de celda: lo manda el
// cliente y no se copia tal cual.
const invalidTargetAudit = "invalid"

var targetCellRefusals = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "cell_target_refusals_total",
	Help: "Peticiones con celda destino explicita (X-Target-Cell) que el gateway rechazo sin enviarlas a ninguna celda.",
}, []string{"reason"})

func init() {
	prometheus.MustRegister(targetCellRefusals)
	for _, reason := range []string{targetNotOperator, targetMalformed, targetNotCellService, targetNotServed} {
		targetCellRefusals.WithLabelValues(reason)
	}
}

type targetCtxKey int

const (
	// requestedTargetKey: lo que el cliente mando en X-Target-Cell, sin validar.
	requestedTargetKey targetCtxKey = iota
	// routedTargetKey: la celda ya validada por targetCellGate.
	routedTargetKey
)

type requestedTarget struct{ values []string }

// auditValue es la celda pedida si tiene forma de codigo de celda.
func (t requestedTarget) auditValue() string {
	if len(t.values) == 1 && tenantcell.ValidCode(t.values[0]) {
		return t.values[0]
	}
	return invalidTargetAudit
}

// captureTargetCell retira X-Target-Cell de toda peticion y guarda lo pedido en el contexto: no
// llega a ningun servicio, tampoco por las rutas publicas ni por las que autentica el servicio,
// donde no significa nada.
func captureTargetCell(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if values, ok := r.Header[http.CanonicalHeaderKey(targetCellHeader)]; ok {
			r.Header.Del(targetCellHeader)
			r = r.WithContext(context.WithValue(r.Context(), requestedTargetKey, requestedTarget{values: values}))
		}
		next.ServeHTTP(w, r)
	})
}

func requestedTargetFrom(ctx context.Context) (requestedTarget, bool) {
	t, ok := ctx.Value(requestedTargetKey).(requestedTarget)
	return t, ok
}

func routedTargetFrom(ctx context.Context) (string, bool) {
	cell, ok := ctx.Value(routedTargetKey).(string)
	return cell, ok
}

// targetCellGate aplica las reglas de la celda destino en las rutas con sesion.
type targetCellGate struct {
	services     map[string]string
	cellServices map[string]bool
	targets      map[string]map[string]string
	baseCell     string
	logger       *zap.Logger
}

func newTargetCellGate(t *routeTable, logger *zap.Logger) *targetCellGate {
	g := &targetCellGate{
		services:     make(map[string]string, len(t.Routes)),
		cellServices: map[string]bool{},
		targets:      t.cellTargets,
		baseCell:     t.baseCell,
		logger:       logger,
	}
	for _, rt := range t.Routes {
		g.services[rt.Prefix] = rt.Service
	}
	for name, s := range t.Services {
		if s.CellHostsEnv != "" {
			g.cellServices[name] = true
		}
	}
	return g
}

// served: la celda tiene instancia declarada del servicio. Sin celda base el gateway no sabe que
// celda sirve el destino base, asi que no sirve ninguna celda destino.
func (g *targetCellGate) served(service, cell string) bool {
	if g.baseCell == "" {
		return false
	}
	if cell == g.baseCell {
		return true
	}
	_, ok := g.targets[service][cell]
	return ok
}

func (g *targetCellGate) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req, ok := requestedTargetFrom(r.Context())
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		seg1, _ := pathSegments(r.URL.Path)
		service := g.services[seg1]
		switch {
		case !isPlatformOperator(r.Context()):
			g.refuse(w, r, targetNotOperator, req, http.StatusForbidden, codeTargetCellForbidden, "solo un operador de la plataforma elige la celda destino")
		case req.auditValue() == invalidTargetAudit:
			g.refuse(w, r, targetMalformed, req, http.StatusBadRequest, codeInvalidTargetCell, "la celda destino no es un codigo de celda")
		case !g.cellServices[service]:
			g.refuse(w, r, targetNotCellService, req, http.StatusBadRequest, codeTargetCellNotApplicable, "la ruta no es de un servicio de celda")
		case !g.served(service, req.values[0]):
			g.refuse(w, r, targetNotServed, req, http.StatusServiceUnavailable, tenantcell.CodeCellUnavailable, "el servicio no esta disponible en la celda destino")
		default:
			cell := req.values[0]
			g.logger.Info("celdas: operador con celda destino",
				zap.String("user_id", middleware.GetUserID(r.Context())), zap.String("service", service),
				zap.String("cell", cell), zap.String("method", r.Method), zap.String("path", r.URL.Path))
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), routedTargetKey, cell)))
		}
	})
}

func (g *targetCellGate) refuse(w http.ResponseWriter, r *http.Request, reason string, req requestedTarget, status int, code, msg string) {
	targetCellRefusals.WithLabelValues(reason).Inc()
	w.Header().Set("Cache-Control", "no-store")
	g.logger.Warn("celdas: celda destino rechazada",
		zap.String("reason", reason), zap.String("user_id", middleware.GetUserID(r.Context())),
		zap.String("tenant_id", middleware.GetTenantID(r.Context())), zap.String("cell", req.auditValue()),
		zap.String("method", r.Method), zap.String("path", r.URL.Path))
	response.Err(w, status, code, msg)
}

// isPlatformOperator: el token verificado lleva el rol superadmin.
func isPlatformOperator(ctx context.Context) bool {
	for _, role := range middleware.GetRoles(ctx) {
		if role == middleware.RoleSuperadmin {
			return true
		}
	}
	return false
}

// varyByTargetCell: la respuesta de un servicio de celda depende de la celda destino, y una
// cache del navegador no debe servir la de una celda a la peticion de otra.
func varyByTargetCell(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", targetCellHeader)
		next.ServeHTTP(w, r)
	})
}
