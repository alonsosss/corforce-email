package http

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/services/observability/internal/app"
	"github.com/alonsosss/corforce-email/services/observability/internal/domain"
	"github.com/go-chi/chi/v5"
)

// Modulo de permisos que gatea el gateway y que exigen los handlers.
const permissionModule = "observability"

// adminPrefix es el prefijo del API de administracion, el que enruta el gateway.
const adminPrefix = "/api/v1/observability"

// logQueriesPerMinute es el cupo por usuario de consultas de registros, por debajo del limite general
// del servicio: cada consulta recorre en Loki hasta 24 horas de un servicio.
const logQueriesPerMinute = 30

// Handler sirve el API de administracion tras el gateway.
type Handler struct {
	logs    *app.LogsUseCase
	authz   *authz.Checker
	limiter *middleware.RateLimiter
}

func NewHandler(logs *app.LogsUseCase, checker *authz.Checker) *Handler {
	return &Handler{logs: logs, authz: checker, limiter: middleware.NewRateLimiter(logQueriesPerMinute, time.Minute)}
}

func (h *Handler) can(resource, action string) func(http.Handler) http.Handler {
	return h.authz.RequirePermission(permissionModule, resource, action)
}

// Routes: todo lo que no es la salud exige el rol superadmin en el servicio, ademas del permiso de
// plataforma; el caso de uso lo vuelve a comprobar.
func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Route(adminPrefix, func(r chi.Router) {
		r.Get("/health", h.Health)
		r.Group(func(r chi.Router) {
			r.Use(middleware.RequireRoles(middleware.RoleSuperadmin))
			r.Use(h.can("logs", "read"))
			r.Get("/logs/services", h.ListLogServices)
			r.With(h.limiter.LimitPerUser).Get("/logs", h.QueryLogs)
		})
	})
	return r
}

func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	response.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// platformFrom dice si quien llama opera la plataforma: HasAnyRole sin roles adicionales solo deja pasar
// al superadmin.
func platformFrom(r *http.Request) bool { return middleware.HasAnyRole(r.Context()) }

// ListLogServices devuelve la lista blanca de servicios consultables, ordenada.
func (h *Handler) ListLogServices(w http.ResponseWriter, r *http.Request) {
	out, err := h.logs.Services(platformFrom(r))
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string][]string{"services": out})
}

// QueryLogs: ?service=&q=&since=&until=&limit=&direction=. since y until en RFC 3339; por defecto la
// ultima hora, como mucho 24 horas y 500 lineas.
func (h *Handler) QueryLogs(w http.ResponseWriter, r *http.Request) {
	params := r.URL.Query()
	in := domain.LogQueryInput{Service: params.Get("service"), Text: params.Get("q"), Direction: params.Get("direction")}
	var ok bool
	if in.Since, ok = timeParam(w, params.Get("since"), "since"); !ok {
		return
	}
	if in.Until, ok = timeParam(w, params.Get("until"), "until"); !ok {
		return
	}
	if raw := params.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			response.ErrBadRequest(w, "limit debe ser un entero positivo")
			return
		}
		in.Limit = n
	}
	out, err := h.logs.Query(r.Context(), platformFrom(r), middleware.GetUserID(r.Context()), in)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, out)
}

func timeParam(w http.ResponseWriter, raw, name string) (*time.Time, bool) {
	if raw == "" {
		return nil, true
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		response.ErrBadRequest(w, name+" debe ser una fecha RFC 3339")
		return nil, false
	}
	return &t, true
}

func writeError(w http.ResponseWriter, err error) {
	var verr *domain.ValidationError
	switch {
	case errors.Is(err, domain.ErrPlatformOnly):
		response.ErrForbidden(w, err.Error())
	case errors.As(err, &verr):
		response.ErrValidation(w, verr.Msg)
	case errors.Is(err, domain.ErrNotConfigured):
		response.Err(w, http.StatusServiceUnavailable, "NOT_CONFIGURED", err.Error())
	case errors.Is(err, domain.ErrStoreUnavailable), errors.Is(err, domain.ErrStoreRejected):
		response.Err(w, http.StatusBadGateway, "LOGS_UNAVAILABLE", "el almacén de registros no pudo atender la consulta")
	default:
		response.Unexpected(w, err)
	}
}
