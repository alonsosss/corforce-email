package http

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/access-control/internal/app"
	"github.com/alonsosss/corforce-email/services/access-control/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type Handler struct {
	rbac *app.RBACUseCase
}

func NewHandler(rbac *app.RBACUseCase) *Handler {
	return &Handler{rbac: rbac}
}

func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()

	r.Route("/api/v1", func(r chi.Router) {
		r.Route("/roles", func(r chi.Router) {
			r.With(h.perm("roles", "read")).Get("/", h.ListRoles)
			r.With(h.perm("roles", "create")).Post("/", h.CreateRole)
			r.With(h.perm("roles", "read")).Get("/{id}", h.GetRole)
			r.With(h.perm("roles", "update")).Put("/{id}", h.UpdateRole)
			r.With(h.perm("roles", "delete")).Delete("/{id}", h.DeleteRole)
			r.With(h.perm("roles", "update")).Put("/{id}/permissions", h.SetRolePermissions)
			r.With(h.perm("roles", "read")).Get("/{id}/permissions", h.GetRolePermissions)
		})

		r.Route("/permissions", func(r chi.Router) {
			r.With(h.perm("permissions", "read")).Get("/", h.ListPermissions)
		})

		r.Route("/user-roles", func(r chi.Router) {
			r.With(h.perm("user_roles", "assign")).Post("/assign", h.AssignRole)
			r.With(h.perm("user_roles", "revoke")).Post("/revoke", h.RevokeRole)
			r.With(h.selfOrPerm("userID", "user_roles", "read")).Get("/user/{userID}", h.GetUserRoles)
		})

		// Consultar el acceso propio es autoservicio; el de otro usuario exige ver sus roles.
		r.Post("/check-access", h.CheckAccess)
		r.With(h.selfOrPerm("userID", "user_roles", "read")).Get("/policy/{userID}", h.GetAccessPolicy)
		// Acceso operativo del usuario actual (para filtrar el menu segun su rol).
		r.Get("/access/my-modules", h.MyModules)
		// Metricas de accesos denegados por RBAC: registro (interno, desde el gateway)
		// y consulta (administracion).
		r.With(middleware.RequireInternalCaller).Post("/access/denials", h.RecordDenial)
		r.With(h.perm("denials", "read")).Get("/access/denials", h.GetDenials)
		// Destinatarios de un aviso, resueltos por permiso (uso interno entre servicios).
		r.With(h.internalOrPerm("user_roles", "read")).Get("/access/users-with-permission", h.UsersWithPermission)
	})

	return r
}

// requestTenant devuelve el tenant inyectado por el gateway o responde 401 y false.
func requestTenant(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	tenantID, err := uuid.Parse(middleware.GetTenantID(r.Context()))
	if err != nil {
		response.ErrUnauthorized(w, "invalid tenant")
		return uuid.Nil, false
	}
	return tenantID, true
}

// requestUser devuelve el usuario inyectado por el gateway o responde 401 y false.
// requestActor es el usuario que pide un cambio de roles y si tiene un rol del sistema.
func requestActor(w http.ResponseWriter, r *http.Request) (app.Actor, bool) {
	userID, ok := requestUser(w, r)
	if !ok {
		return app.Actor{}, false
	}
	return app.Actor{UserID: userID, Privileged: middleware.IsPrivileged(r.Context())}, true
}

func writeRoleGrantError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrRoleNotFound):
		response.ErrNotFound(w, "role not found")
	case errors.Is(err, domain.ErrSystemRoleAssignment), errors.Is(err, domain.ErrPermissionNotHeld):
		response.ErrForbidden(w, err.Error())
	default:
		response.ErrInternal(w)
	}
}

func requestUser(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	userID, err := uuid.Parse(middleware.GetUserID(r.Context()))
	if err != nil {
		response.ErrUnauthorized(w, "invalid user")
		return uuid.Nil, false
	}
	return userID, true
}

type createRoleRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (h *Handler) CreateRole(w http.ResponseWriter, r *http.Request) {
	var req createRoleRequest
	if err := validate.DecodeJSON(r, &req); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}

	v := validate.New()
	v.Required("name", req.Name)
	v.MaxLength("name", req.Name, 100)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}

	tenantID, ok := requestTenant(w, r)
	if !ok {
		return
	}

	role, err := h.rbac.CreateRole(r.Context(), tenantID, req.Name, req.Description)
	if err != nil {
		if err == domain.ErrRoleAlreadyExists {
			response.ErrConflict(w, "role already exists")
			return
		}
		response.ErrInternal(w)
		return
	}

	response.JSON(w, http.StatusCreated, roleResponse(role))
}

func (h *Handler) GetRole(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.ErrBadRequest(w, "invalid role id")
		return
	}

	tenantID, ok := requestTenant(w, r)
	if !ok {
		return
	}

	role, err := h.rbac.GetRole(r.Context(), tenantID, id)
	if err != nil {
		response.ErrNotFound(w, "role not found")
		return
	}

	response.JSON(w, http.StatusOK, roleResponse(role))
}

func (h *Handler) ListRoles(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := requestTenant(w, r)
	if !ok {
		return
	}

	roles, err := h.rbac.ListRoles(r.Context(), tenantID)
	if err != nil {
		response.ErrInternal(w)
		return
	}

	response.JSON(w, http.StatusOK, rolesResponse(roles))
}

func (h *Handler) UpdateRole(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.ErrBadRequest(w, "invalid role id")
		return
	}

	var req createRoleRequest
	if err := validate.DecodeJSON(r, &req); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}

	v := validate.New()
	v.Required("name", req.Name)
	v.MaxLength("name", req.Name, 100)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}

	tenantID, ok := requestTenant(w, r)
	if !ok {
		return
	}

	role, err := h.rbac.UpdateRole(r.Context(), tenantID, id, req.Name, req.Description)
	if err != nil {
		switch err {
		case domain.ErrRoleNotFound:
			response.ErrNotFound(w, "role not found")
		case domain.ErrSystemRole:
			response.ErrForbidden(w, "cannot modify system role")
		default:
			response.ErrInternal(w)
		}
		return
	}

	response.JSON(w, http.StatusOK, roleResponse(role))
}

func (h *Handler) DeleteRole(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.ErrBadRequest(w, "invalid role id")
		return
	}

	tenantID, ok := requestTenant(w, r)
	if !ok {
		return
	}

	if err := h.rbac.DeleteRole(r.Context(), tenantID, id); err != nil {
		switch err {
		case domain.ErrRoleNotFound:
			response.ErrNotFound(w, "role not found")
		case domain.ErrSystemRole:
			response.ErrForbidden(w, "cannot delete system role")
		default:
			response.ErrInternal(w)
		}
		return
	}

	response.JSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

type setPermissionsRequest struct {
	PermissionIDs []string `json:"permission_ids"`
}

func (h *Handler) SetRolePermissions(w http.ResponseWriter, r *http.Request) {
	roleID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.ErrBadRequest(w, "invalid role id")
		return
	}

	var req setPermissionsRequest
	if err := validate.DecodeJSON(r, &req); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}

	tenantID, ok := requestTenant(w, r)
	if !ok {
		return
	}

	permIDs := make([]uuid.UUID, 0, len(req.PermissionIDs))
	for _, pid := range req.PermissionIDs {
		id, err := uuid.Parse(pid)
		if err != nil {
			response.ErrBadRequest(w, "invalid permission id: "+pid)
			return
		}
		permIDs = append(permIDs, id)
	}

	actor, ok := requestActor(w, r)
	if !ok {
		return
	}
	if err := h.rbac.SetRolePermissions(r.Context(), actor, tenantID, roleID, permIDs); err != nil {
		switch err {
		case domain.ErrRoleNotFound:
			response.ErrNotFound(w, "role not found")
		case domain.ErrSystemRole:
			response.ErrForbidden(w, "cannot modify system role permissions")
		case domain.ErrPermissionNotFound:
			response.ErrBadRequest(w, "unknown permission id")
		case domain.ErrPlatformPermission, domain.ErrPermissionNotHeld:
			response.ErrForbidden(w, err.Error())
		default:
			response.ErrInternal(w)
		}
		return
	}

	response.JSON(w, http.StatusOK, map[string]string{"status": "permissions updated"})
}

func (h *Handler) GetRolePermissions(w http.ResponseWriter, r *http.Request) {
	roleID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.ErrBadRequest(w, "invalid role id")
		return
	}

	tenantID, ok := requestTenant(w, r)
	if !ok {
		return
	}

	perms, err := h.rbac.GetRolePermissions(r.Context(), tenantID, roleID)
	if err != nil {
		if err == domain.ErrRoleNotFound {
			response.ErrNotFound(w, "role not found")
			return
		}
		response.ErrInternal(w)
		return
	}

	response.JSON(w, http.StatusOK, permsResponse(perms))
}

func (h *Handler) ListPermissions(w http.ResponseWriter, r *http.Request) {
	module := r.URL.Query().Get("module")
	var perms []*domain.Permission
	var err error

	includePlatform := middleware.HasAnyRole(r.Context(), middleware.RoleSuperadmin)
	if module != "" {
		perms, err = h.rbac.ListPermissionsByModule(r.Context(), module, includePlatform)
	} else {
		perms, err = h.rbac.ListPermissions(r.Context(), includePlatform)
	}
	if err != nil {
		response.ErrInternal(w)
		return
	}

	response.JSON(w, http.StatusOK, permsResponse(perms))
}

type userRoleRequest struct {
	UserID string `json:"user_id"`
	RoleID string `json:"role_id"`
}

func (req userRoleRequest) parse(w http.ResponseWriter) (userID, roleID uuid.UUID, ok bool) {
	userID, err := uuid.Parse(req.UserID)
	if err != nil {
		response.ErrBadRequest(w, "invalid user_id")
		return uuid.Nil, uuid.Nil, false
	}
	roleID, err = uuid.Parse(req.RoleID)
	if err != nil {
		response.ErrBadRequest(w, "invalid role_id")
		return uuid.Nil, uuid.Nil, false
	}
	return userID, roleID, true
}

func (h *Handler) AssignRole(w http.ResponseWriter, r *http.Request) {
	var req userRoleRequest
	if err := validate.DecodeJSON(r, &req); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	userID, roleID, ok := req.parse(w)
	if !ok {
		return
	}
	tenantID, ok := requestTenant(w, r)
	if !ok {
		return
	}
	actor, ok := requestActor(w, r)
	if !ok {
		return
	}

	if err := h.rbac.AssignRoleToUser(r.Context(), actor, tenantID, userID, roleID); err != nil {
		writeRoleGrantError(w, err)
		return
	}

	response.JSON(w, http.StatusOK, map[string]string{"status": "role assigned"})
}

func (h *Handler) RevokeRole(w http.ResponseWriter, r *http.Request) {
	var req userRoleRequest
	if err := validate.DecodeJSON(r, &req); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	userID, roleID, ok := req.parse(w)
	if !ok {
		return
	}
	tenantID, ok := requestTenant(w, r)
	if !ok {
		return
	}

	actor, ok := requestActor(w, r)
	if !ok {
		return
	}
	if err := h.rbac.RevokeRoleFromUser(r.Context(), actor, tenantID, userID, roleID); err != nil {
		writeRoleGrantError(w, err)
		return
	}

	response.JSON(w, http.StatusOK, map[string]string{"status": "role revoked"})
}

func (h *Handler) GetUserRoles(w http.ResponseWriter, r *http.Request) {
	userID, err := uuid.Parse(chi.URLParam(r, "userID"))
	if err != nil {
		response.ErrBadRequest(w, "invalid user id")
		return
	}

	tenantID, ok := requestTenant(w, r)
	if !ok {
		return
	}

	roles, err := h.rbac.GetUserRoles(r.Context(), userID, tenantID)
	if err != nil {
		response.ErrInternal(w)
		return
	}

	response.JSON(w, http.StatusOK, rolesResponse(roles))
}

type checkAccessRequest struct {
	UserID   string `json:"user_id"`
	Module   string `json:"module"`
	Resource string `json:"resource"`
	Action   string `json:"action"`
}

func (h *Handler) CheckAccess(w http.ResponseWriter, r *http.Request) {
	var req checkAccessRequest
	if err := validate.DecodeJSON(r, &req); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}

	userID, err := uuid.Parse(req.UserID)
	if err != nil {
		response.ErrBadRequest(w, "invalid user_id")
		return
	}
	if !isSelf(r, userID) && !h.allowed(w, r, "user_roles", "read") {
		return
	}

	tenantID, ok := requestTenant(w, r)
	if !ok {
		return
	}

	allowed, err := h.rbac.CheckAccess(r.Context(), userID, tenantID, req.Module, req.Resource, req.Action)
	if err != nil {
		response.ErrInternal(w)
		return
	}

	response.JSON(w, http.StatusOK, map[string]bool{"allowed": allowed})
}

// MyModules devuelve el acceso operativo del usuario autenticado: los modulos en los
// que tiene permisos, sus acciones de escritura, sus roles y si es administrador. El
// shell lo usa para mostrar en el menu solo lo que corresponde a su rol y el gateway
// para gatear escrituras y validar la sesion.
//
// Contrato con el gateway: 404 USER_NOT_FOUND y 403 USER_NOT_ACTIVE son definitivos (el
// gateway rechaza el token con 401); cualquier otra respuesta que no sea 200 significa
// que no se pudo determinar.
func (h *Handler) MyModules(w http.ResponseWriter, r *http.Request) {
	userID, ok := requestUser(w, r)
	if !ok {
		return
	}
	tenantID, ok := requestTenant(w, r)
	if !ok {
		return
	}

	access, err := h.rbac.GetUserAccess(r.Context(), userID, tenantID)
	switch {
	case errors.Is(err, domain.ErrUserNotFound):
		response.Err(w, http.StatusNotFound, "USER_NOT_FOUND", "user not found")
		return
	case errors.Is(err, domain.ErrUserNotActive):
		response.Err(w, http.StatusForbidden, "USER_NOT_ACTIVE", "user not active")
		return
	case err != nil:
		response.ErrInternal(w)
		return
	}

	response.JSON(w, http.StatusOK, map[string]interface{}{
		"is_admin":          access.IsAdmin,
		"roles":             access.Roles,
		"modules":           access.Modules,
		"write_modules":     access.WriteModules,
		"write_actions":     access.WriteActions,
		"disabled_modules":  access.DisabledModules,
		"tokens_valid_from": access.TokensValidFrom.UTC().Format(time.RFC3339),
	})
}

// UsersWithPermission lista los usuarios de la empresa que pueden hacer algo concreto.
// Lo consumen los servicios que necesitan avisar a quien corresponde.
func (h *Handler) UsersWithPermission(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := requestTenant(w, r)
	if !ok {
		return
	}
	module := r.URL.Query().Get("module")
	action := r.URL.Query().Get("action")
	if module == "" || action == "" {
		response.ErrBadRequest(w, "module y action son obligatorios")
		return
	}
	users, err := h.rbac.UsersWithPermission(r.Context(), tenantID, module, action)
	if err != nil {
		response.ErrInternal(w)
		return
	}
	ids := make([]string, 0, len(users))
	for _, u := range users {
		ids = append(ids, u.String())
	}
	response.JSON(w, http.StatusOK, map[string]interface{}{"user_ids": ids})
}

type recordDenialRequest struct {
	TenantID string `json:"tenant_id"`
	UserID   string `json:"user_id"`
	Module   string `json:"module"`
	Action   string `json:"action"`
	Method   string `json:"method"`
	Path     string `json:"path"`
	Enforced bool   `json:"enforced"`
}

// RecordDenial registra un acceso denegado por RBAC. Lo invoca el gateway (interno,
// con X-Gateway-Token) de forma best-effort; nunca debe afectar a la peticion original.
func (h *Handler) RecordDenial(w http.ResponseWriter, r *http.Request) {
	var req recordDenialRequest
	if err := validate.DecodeJSON(r, &req); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	tenantID, err := uuid.Parse(req.TenantID)
	if err != nil {
		response.ErrBadRequest(w, "invalid tenant_id")
		return
	}
	userID, err := uuid.Parse(req.UserID)
	if err != nil {
		response.ErrBadRequest(w, "invalid user_id")
		return
	}
	_ = h.rbac.RecordDenial(r.Context(), &domain.AccessDenial{
		TenantID: tenantID,
		UserID:   userID,
		Module:   req.Module,
		Action:   req.Action,
		Method:   req.Method,
		Path:     req.Path,
		Enforced: req.Enforced,
	})
	w.WriteHeader(http.StatusAccepted)
}

// GetDenials devuelve las metricas de accesos denegados del tenant (administracion).
// Acepta ?days=N (ventana, por defecto 7) y ?limit=N (recientes, por defecto 50).
func (h *Handler) GetDenials(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := requestTenant(w, r)
	if !ok {
		return
	}

	days := 7
	if d, err := strconv.Atoi(r.URL.Query().Get("days")); err == nil && d > 0 && d <= 90 {
		days = d
	}
	limit := 50
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 && l <= 200 {
		limit = l
	}
	since := time.Now().AddDate(0, 0, -days)

	m, err := h.rbac.DenialMetrics(r.Context(), tenantID, since, limit)
	if err != nil {
		response.ErrInternal(w)
		return
	}

	recent := make([]map[string]interface{}, 0, len(m.Recent))
	for _, d := range m.Recent {
		recent = append(recent, map[string]interface{}{
			"user_id": d.UserID.String(), "module": d.Module, "action": d.Action,
			"method": d.Method, "path": d.Path, "enforced": d.Enforced, "created_at": d.CreatedAt,
		})
	}

	response.JSON(w, http.StatusOK, map[string]interface{}{
		"days":      days,
		"total":     m.Total,
		"by_module": summaryResponse(m.ByModule),
		"by_user":   summaryResponse(m.ByUser),
		"recent":    recent,
	})
}

func (h *Handler) GetAccessPolicy(w http.ResponseWriter, r *http.Request) {
	userID, err := uuid.Parse(chi.URLParam(r, "userID"))
	if err != nil {
		response.ErrBadRequest(w, "invalid user id")
		return
	}

	tenantID, ok := requestTenant(w, r)
	if !ok {
		return
	}

	policy, err := h.rbac.GetAccessPolicy(r.Context(), userID, tenantID)
	if err != nil {
		response.ErrInternal(w)
		return
	}

	response.JSON(w, http.StatusOK, map[string]interface{}{
		"user_id":     policy.UserID.String(),
		"tenant_id":   policy.TenantID.String(),
		"roles":       policy.RoleNames(),
		"permissions": formatPermissions(policy.Permissions),
	})
}

func roleResponse(r *domain.Role) map[string]interface{} {
	return map[string]interface{}{
		"id":          r.ID.String(),
		"name":        r.Name,
		"description": r.Description,
		"is_system":   r.IsSystem,
		"status":      r.Status,
		"created_at":  r.CreatedAt,
		"updated_at":  r.UpdatedAt,
	}
}

func rolesResponse(roles []*domain.Role) []map[string]interface{} {
	items := make([]map[string]interface{}, 0, len(roles))
	for _, role := range roles {
		items = append(items, roleResponse(role))
	}
	return items
}

func permResponse(p *domain.Permission) map[string]interface{} {
	return map[string]interface{}{
		"id":          p.ID.String(),
		"module":      p.Module,
		"resource":    p.Resource,
		"action":      p.Action,
		"description": p.Description,
		"scope":       p.Scope,
	}
}

func permsResponse(perms []*domain.Permission) []map[string]interface{} {
	items := make([]map[string]interface{}, 0, len(perms))
	for _, p := range perms {
		items = append(items, permResponse(p))
	}
	return items
}

func formatPermissions(perms []domain.Permission) []map[string]string {
	result := make([]map[string]string, 0, len(perms))
	for _, p := range perms {
		result = append(result, map[string]string{
			"module":   p.Module,
			"resource": p.Resource,
			"action":   p.Action,
		})
	}
	return result
}

func summaryResponse(rows []domain.DenialSummaryRow) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(rows))
	for _, row := range rows {
		out = append(out, map[string]interface{}{"key": row.Key, "count": row.Count})
	}
	return out
}
