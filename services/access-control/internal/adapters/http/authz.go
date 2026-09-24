package http

import (
	"net/http"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// Tercera capa de control para las rutas de access-control. La semantica es la de
// pkg/authz.Checker.RequirePermission (los roles del sistema pasan, 403 sin permiso, 503
// si la politica no se puede leer), pero resuelta en proceso con el caso de uso: el
// servicio que custodia la politica no puede preguntarse a si mismo por HTTP.

const permModule = "access"

// allowed responde y devuelve false cuando el usuario de la peticion no tiene el permiso.
func (h *Handler) allowed(w http.ResponseWriter, r *http.Request, resource, action string) bool {
	ctx := r.Context()
	if middleware.IsPrivileged(ctx) {
		return true
	}
	userID, errUser := uuid.Parse(middleware.GetUserID(ctx))
	tenantID, errTenant := uuid.Parse(middleware.GetTenantID(ctx))
	if errUser != nil || errTenant != nil {
		response.ErrForbidden(w, "su rol no tiene permiso para esta operacion")
		return false
	}
	ok, err := h.rbac.CheckAccess(ctx, userID, tenantID, permModule, resource, action)
	if err != nil {
		response.Err(w, http.StatusServiceUnavailable, "AUTHZ_UNAVAILABLE", "no se pudo comprobar el permiso")
		return false
	}
	if !ok {
		response.ErrForbidden(w, "su rol no tiene permiso para esta operacion")
		return false
	}
	return true
}

func (h *Handler) perm(resource, action string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if h.allowed(w, r, resource, action) {
				next.ServeHTTP(w, r)
			}
		})
	}
}

// isSelf indica si el usuario de la peticion es el indicado.
func isSelf(r *http.Request, userID uuid.UUID) bool {
	caller, err := uuid.Parse(middleware.GetUserID(r.Context()))
	return err == nil && caller == userID
}

// selfOrPerm deja consultar lo propio (el usuario del parametro de ruta) y exige el
// permiso para consultar lo de otro. pkg/authz pide /policy/{uid} con X-User-ID = uid,
// asi que los demas servicios caen siempre en el caso propio.
func (h *Handler) selfOrPerm(param, resource, action string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if id, err := uuid.Parse(chi.URLParam(r, param)); err == nil && isSelf(r, id) {
				next.ServeHTTP(w, r)
				return
			}
			if h.allowed(w, r, resource, action) {
				next.ServeHTTP(w, r)
			}
		})
	}
}

// internalOrPerm: una llamada entre servicios llega sin usuario, ya autenticada por el
// token interno (RequireGatewayToken); una que trae usuario viene del gateway en nombre
// de una persona y necesita el permiso.
func (h *Handler) internalOrPerm(resource, action string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			internal := middleware.GetUserID(r.Context()) == "" && middleware.GetAPIKeyID(r.Context()) == ""
			if internal || h.allowed(w, r, resource, action) {
				next.ServeHTTP(w, r)
			}
		})
	}
}
