package http

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/services/access-control/internal/app"
	"github.com/alonsosss/corforce-email/services/access-control/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// InternalHandler sirve en /internal/access-control las operaciones de roles sobre una
// empresa entera que organization orquesta al darla de alta y de baja. Solo las llaman otros
// servicios: el router exige el token interno y RequireInternalCaller rechaza cualquier
// peticion que traiga usuario. Todas son idempotentes: repetirlas con la misma empresa
// responde lo mismo sin repetir el efecto.
type InternalHandler struct {
	uc *app.TenantRolesUseCase
}

func NewInternalHandler(uc *app.TenantRolesUseCase) *InternalHandler {
	return &InternalHandler{uc: uc}
}

func (h *InternalHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.RequireInternalCaller)
	r.Post("/system-role/reseed", h.ReseedSystemRoles)
	r.Route("/tenants/{tenantID}", func(r chi.Router) {
		r.Put("/system-role", h.SeedSystemRole)
		r.Delete("/roles", h.RemoveTenantRoles)
		r.Put("/users/{userID}/roles/{roleID}", h.AssignRole)
		r.Delete("/users/{userID}/roles/{roleID}", h.RevokeRole)
	})
	return r
}

// pathIDs lee los identificadores de la ruta; responde 400 y false si alguno no es valido.
func pathIDs(w http.ResponseWriter, r *http.Request, params ...string) ([]uuid.UUID, bool) {
	ids := make([]uuid.UUID, 0, len(params))
	for _, p := range params {
		id, err := uuid.Parse(chi.URLParam(r, p))
		if err != nil {
			response.ErrBadRequest(w, "invalid "+p)
			return nil, false
		}
		ids = append(ids, id)
	}
	return ids, true
}

func (h *InternalHandler) SeedSystemRole(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "tenantID")
	if !ok {
		return
	}
	seed, err := h.uc.SeedSystemRole(r.Context(), ids[0])
	if errors.Is(err, domain.ErrSystemRoleNameTaken) {
		response.Err(w, http.StatusConflict, "SYSTEM_ROLE_NAME_TAKEN", err.Error())
		return
	}
	if err != nil {
		response.Unexpected(w, fmt.Errorf("sembrar el rol del sistema: %w", err))
		return
	}
	response.JSON(w, http.StatusOK, map[string]interface{}{
		"role_id":             seed.Role.ID.String(),
		"name":                seed.Role.Name,
		"created":             seed.Created,
		"permissions_granted": seed.Granted,
	})
}

func (h *InternalHandler) ReseedSystemRoles(w http.ResponseWriter, r *http.Request) {
	res, err := h.uc.ReseedSystemRoles(r.Context())
	if err != nil {
		response.Unexpected(w, fmt.Errorf("resembrar los roles del sistema: %w", err))
		return
	}
	response.JSON(w, http.StatusOK, map[string]int64{"roles": res.Roles, "permissions_granted": res.Granted})
}

func (h *InternalHandler) AssignRole(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "tenantID", "userID", "roleID")
	if !ok {
		return
	}
	err := h.uc.AssignRole(r.Context(), ids[0], ids[1], ids[2])
	if errors.Is(err, domain.ErrRoleNotFound) {
		response.ErrNotFound(w, "role not found")
		return
	}
	if err != nil {
		response.Unexpected(w, fmt.Errorf("asignar rol: %w", err))
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": "assigned"})
}

func (h *InternalHandler) RevokeRole(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "tenantID", "userID", "roleID")
	if !ok {
		return
	}
	if err := h.uc.RevokeRole(r.Context(), ids[0], ids[1], ids[2]); err != nil {
		response.Unexpected(w, fmt.Errorf("retirar rol: %w", err))
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

func (h *InternalHandler) RemoveTenantRoles(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "tenantID")
	if !ok {
		return
	}
	removal, err := h.uc.RemoveTenantRoles(r.Context(), ids[0])
	if errors.Is(err, domain.ErrTenantActive) {
		response.Err(w, http.StatusConflict, "TENANT_ACTIVE", "los roles de una empresa activa no se retiran")
		return
	}
	if err != nil {
		response.Unexpected(w, fmt.Errorf("retirar los roles de la empresa: %w", err))
		return
	}
	response.JSON(w, http.StatusOK, map[string]int64{
		"roles_removed":  removal.Roles,
		"users_affected": int64(len(removal.Users)),
	})
}
