package http

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/organization/internal/app"
	"github.com/alonsosss/corforce-email/services/organization/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type Handler struct {
	uc *app.OrganizationUseCase
}

func NewHandler(uc *app.OrganizationUseCase) *Handler {
	return &Handler{uc: uc}
}

func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()

	r.Route("/api/v1", func(r chi.Router) {
		r.Route("/organizations", func(r chi.Router) {
			// Lectura: el superadmin ve todos los tenants; un usuario de tenant, solo el
			// suyo. El resto del plano de control es exclusivo del superadmin.
			r.Get("/", h.ListTenants)
			r.Get("/{id}", h.GetTenant)
			// Reparar los permisos del rol de sistema: el superadmin sobre cualquier
			// tenant, el administrador sobre el suyo.
			r.With(middleware.RequireRoles(middleware.RoleTenantAdmin)).Post("/{id}/reseed-roles", h.ReseedRoles)

			r.Group(func(r chi.Router) {
				r.Use(middleware.RequireRoles(middleware.RoleSuperadmin))
				r.Post("/", h.CreateTenant)
				r.Patch("/{id}", h.UpdateTenant)
				r.Delete("/{id}", h.DeleteTenant)
				// Migraciones canonicas: barrido de todos, estado por tenant y reintento
				// de uno solo.
				r.Post("/migrate", h.MigrateTenants)
				r.Get("/migrations/status", h.TenantMigrationStatus)
				r.Post("/{id}/migrate", h.MigrateTenant)
				// Modulos habilitados por tenant.
				r.Get("/{id}/modules", h.GetTenantModules)
				r.Put("/{id}/modules", h.SetTenantModule)
			})
		})

		// Directorio de celdas: solo plataforma.
		r.Route("/cells", func(r chi.Router) {
			r.Use(middleware.RequireRoles(middleware.RoleSuperadmin))
			r.Get("/", h.ListCells)
			r.Post("/", h.CreateCell)
			r.Patch("/{id}", h.UpdateCell)
		})
	})

	return r
}

// isSuperadmin indica si quien llama opera la plataforma. HasAnyRole sin roles
// adicionales solo deja pasar al superadmin.
func isSuperadmin(r *http.Request) bool {
	return middleware.HasAnyRole(r.Context())
}

// callerTenantID devuelve el tenant de la sesion, o uuid.Nil si la peticion no lo trae.
func callerTenantID(r *http.Request) uuid.UUID {
	id, err := uuid.Parse(middleware.GetTenantID(r.Context()))
	if err != nil {
		return uuid.Nil
	}
	return id
}

// canReadTenant: el superadmin lee cualquier tenant; los demas, solo el suyo.
func canReadTenant(r *http.Request, id uuid.UUID) bool {
	return isSuperadmin(r) || callerTenantID(r) == id
}

func parseIDParam(w http.ResponseWriter, r *http.Request, what string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.ErrBadRequest(w, "identificador de "+what+" no valido")
		return uuid.Nil, false
	}
	return id, true
}

// ── Tenants ──────────────────────────────────────────────────────────────────

type createTenantRequest struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
	// CellCode es la celda donde crear la base. Ausente = celda por defecto.
	CellCode string `json:"cell_code"`
	// Settings admite solo los ajustes descriptivos enumerados en app.TenantSettingKeys.
	Settings       map[string]string `json:"settings"`
	AdminEmail     string            `json:"admin_email"`
	AdminPassword  string            `json:"admin_password"`
	AdminFirstName string            `json:"admin_first_name"`
	AdminLastName  string            `json:"admin_last_name"`
}

func (h *Handler) CreateTenant(w http.ResponseWriter, r *http.Request) {
	var req createTenantRequest
	if err := validate.DecodeJSON(r, &req); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}

	v := validate.New()
	v.Required("slug", req.Slug)
	v.Slug("slug", req.Slug)
	v.MaxLength("slug", req.Slug, 63)
	v.Required("name", req.Name)
	v.MaxLength("name", req.Name, 255)
	v.Required("admin_email", req.AdminEmail)
	v.Email("admin_email", req.AdminEmail)
	v.MaxLength("admin_email", req.AdminEmail, 255)
	v.Required("admin_password", req.AdminPassword)
	v.MaxLength("admin_first_name", req.AdminFirstName, 100)
	v.MaxLength("admin_last_name", req.AdminLastName, 100)
	v.MaxLength("cell_code", req.CellCode, 63)
	for key := range req.Settings {
		v.OneOf("settings", key, app.TenantSettingKeys())
	}
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}

	tenant, err := h.uc.CreateTenant(r.Context(), app.CreateTenantRequest{
		Slug: req.Slug, Name: req.Name, CellCode: req.CellCode, Settings: req.Settings,
		AdminEmail: req.AdminEmail, AdminPassword: req.AdminPassword,
		AdminFirstName: req.AdminFirstName, AdminLastName: req.AdminLastName,
	})
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrTenantAlreadyExists):
			response.ErrConflict(w, err.Error())
		case errors.Is(err, domain.ErrCellRequired),
			errors.Is(err, domain.ErrAdminUserRequired),
			errors.Is(err, domain.ErrAdminPasswordShort):
			response.ErrValidation(w, err.Error())
		case errors.Is(err, domain.ErrCellNotFound), errors.Is(err, domain.ErrCellNotAssignable):
			response.ErrValidation(w, "cell_code: "+err.Error())
		default:
			response.Unexpected(w, err)
		}
		return
	}

	response.JSON(w, http.StatusCreated, tenantResponse(tenant))
}

func (h *Handler) GetTenant(w http.ResponseWriter, r *http.Request) {
	id, ok := parseIDParam(w, r, "tenant")
	if !ok {
		return
	}
	if !canReadTenant(r, id) {
		response.ErrForbidden(w, "forbidden")
		return
	}
	tenant, err := h.uc.GetTenant(r.Context(), id)
	if err != nil {
		response.ErrNotFound(w, "tenant no encontrado")
		return
	}
	response.JSON(w, http.StatusOK, tenantResponse(tenant))
}

func (h *Handler) ListTenants(w http.ResponseWriter, r *http.Request) {
	// Un usuario de tenant recibe su propio tenant como lista de uno: la misma forma de
	// respuesta sirve a la consola de plataforma y a la pantalla de la organizacion.
	if !isSuperadmin(r) {
		tenantID := callerTenantID(r)
		if tenantID == uuid.Nil {
			response.ErrUnauthorized(w, "la sesion no lleva tenant")
			return
		}
		tenant, err := h.uc.GetTenant(r.Context(), tenantID)
		if err != nil {
			response.ErrNotFound(w, "tenant no encontrado")
			return
		}
		response.JSONWithMeta(w, http.StatusOK, []map[string]interface{}{tenantResponse(tenant)}, response.PageMeta(1, 1, 1))
		return
	}

	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	page, pageSize = app.NormalizePage(page, pageSize)

	tenants, total, err := h.uc.ListTenants(r.Context(), page, pageSize)
	if err != nil {
		response.Unexpected(w, err)
		return
	}
	items := make([]map[string]interface{}, 0, len(tenants))
	for _, t := range tenants {
		items = append(items, tenantResponse(t))
	}
	response.JSONWithMeta(w, http.StatusOK, items, response.PageMeta(total, page, pageSize))
}

type updateTenantRequest struct {
	Name     *string            `json:"name,omitempty"`
	Status   *string            `json:"status,omitempty"`
	Settings map[string]*string `json:"settings,omitempty"`
}

// UpdateTenant cambia nombre, ajustes y estado. Solo superadmin: el estado altera el
// enrutado y los ajustes son parte del alta de plataforma.
func (h *Handler) UpdateTenant(w http.ResponseWriter, r *http.Request) {
	id, ok := parseIDParam(w, r, "tenant")
	if !ok {
		return
	}
	var req updateTenantRequest
	if err := validate.DecodeJSON(r, &req); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	if req.Name == nil && req.Status == nil && len(req.Settings) == 0 {
		response.ErrValidation(w, domain.ErrNothingToUpdate.Error())
		return
	}

	v := validate.New()
	if req.Name != nil {
		v.Required("name", *req.Name)
		v.MaxLength("name", *req.Name, 255)
	}
	if req.Status != nil {
		v.Required("status", *req.Status)
		v.OneOf("status", *req.Status, domain.TenantStatuses())
	}
	for key := range req.Settings {
		v.OneOf("settings", key, app.TenantSettingKeys())
	}
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}

	tenant, err := h.uc.UpdateTenant(r.Context(), id, app.UpdateTenantRequest{Name: req.Name, Settings: req.Settings})
	if err == nil && req.Status != nil {
		tenant, err = h.uc.SetTenantStatus(r.Context(), id, *req.Status)
	}
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrTenantNotFound):
			response.ErrNotFound(w, "tenant no encontrado")
		case errors.Is(err, domain.ErrInvalidTenantStatus):
			response.ErrValidation(w, err.Error())
		default:
			response.Unexpected(w, err)
		}
		return
	}
	response.JSON(w, http.StatusOK, tenantResponse(tenant))
}

func (h *Handler) DeleteTenant(w http.ResponseWriter, r *http.Request) {
	id, ok := parseIDParam(w, r, "tenant")
	if !ok {
		return
	}
	if err := h.uc.DeleteTenant(r.Context(), id); err != nil {
		switch {
		case errors.Is(err, domain.ErrTenantNotFound):
			response.ErrNotFound(w, "tenant no encontrado")
		case errors.Is(err, domain.ErrTenantStillActive):
			response.ErrConflict(w, err.Error())
		default:
			response.Unexpected(w, err)
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ReseedRoles reaplica los permisos del rol de sistema del tenant indicado. El
// middleware ya exige tenant_admin o superadmin; aqui se acota el tenant_admin al suyo.
func (h *Handler) ReseedRoles(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := parseIDParam(w, r, "tenant")
	if !ok {
		return
	}
	if !canReadTenant(r, tenantID) {
		response.ErrForbidden(w, "forbidden")
		return
	}
	if err := h.uc.ReseedRoles(r.Context(), tenantID); err != nil {
		if errors.Is(err, domain.ErrTenantNotFound) {
			response.ErrNotFound(w, "tenant no encontrado")
			return
		}
		response.Unexpected(w, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": "roles reseeded"})
}

// ── Migraciones ──────────────────────────────────────────────────────────────

// MigrateTenants aplica las migraciones canonicas pendientes a todos los tenants
// activos (idempotente).
func (h *Handler) MigrateTenants(w http.ResponseWriter, r *http.Request) {
	res, err := h.uc.MigrateAllTenants(r.Context())
	if err != nil {
		response.Unexpected(w, err)
		return
	}
	response.JSON(w, http.StatusOK, res)
}

// MigrateTenant reintenta las migraciones canonicas de un solo tenant.
func (h *Handler) MigrateTenant(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := parseIDParam(w, r, "tenant")
	if !ok {
		return
	}
	switch err := h.uc.MigrateTenant(r.Context(), tenantID); {
	case err == nil:
		response.JSON(w, http.StatusOK, map[string]string{"status": "migrated"})
	case errors.Is(err, domain.ErrTenantNotFound):
		response.ErrNotFound(w, "tenant no encontrado")
	case errors.Is(err, domain.ErrMigrationsLocked):
		response.ErrConflict(w, "migraciones en curso en otra instancia")
	default:
		response.Unexpected(w, err)
	}
}

// TenantMigrationStatus reporta el estado de las migraciones canonicas de cada tenant
// (aplicadas, pendientes, baselineadas).
func (h *Handler) TenantMigrationStatus(w http.ResponseWriter, r *http.Request) {
	statuses, err := h.uc.TenantMigrationStatuses(r.Context())
	if err != nil {
		response.Unexpected(w, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]interface{}{"tenants": statuses})
}

// ── Modulos ──────────────────────────────────────────────────────────────────

func (h *Handler) GetTenantModules(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := parseIDParam(w, r, "tenant")
	if !ok {
		return
	}
	if _, err := h.uc.GetTenant(r.Context(), tenantID); err != nil {
		response.ErrNotFound(w, "tenant no encontrado")
		return
	}
	modules, err := h.uc.GetTenantModules(r.Context(), tenantID)
	if err != nil {
		response.Unexpected(w, err)
		return
	}
	response.JSON(w, http.StatusOK, modules)
}

type setTenantModuleRequest struct {
	Module  string `json:"module"`
	Enabled bool   `json:"enabled"`
}

// SetTenantModule habilita o deshabilita un modulo del tenant con validacion de
// dependencias y devuelve el catalogo con el estado resultante.
func (h *Handler) SetTenantModule(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := parseIDParam(w, r, "tenant")
	if !ok {
		return
	}
	var req setTenantModuleRequest
	if err := validate.DecodeJSON(r, &req); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	v := validate.New()
	v.Required("module", req.Module)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	if err := h.uc.SetTenantModule(r.Context(), tenantID, req.Module, req.Enabled); err != nil {
		switch {
		case errors.Is(err, domain.ErrTenantNotFound):
			response.ErrNotFound(w, "tenant no encontrado")
		case errors.Is(err, domain.ErrModuleNotFound):
			response.ErrNotFound(w, err.Error())
		case errors.Is(err, domain.ErrModuleCore), errors.Is(err, domain.ErrModuleRequired):
			response.ErrConflict(w, err.Error())
		default:
			response.Unexpected(w, err)
		}
		return
	}
	modules, err := h.uc.GetTenantModules(r.Context(), tenantID)
	if err != nil {
		response.Unexpected(w, err)
		return
	}
	response.JSON(w, http.StatusOK, modules)
}

// ── Respuestas ───────────────────────────────────────────────────────────────

func tenantResponse(t *domain.Tenant) map[string]interface{} {
	res := map[string]interface{}{
		"id":         t.ID.String(),
		"slug":       t.Slug,
		"name":       t.Name,
		"status":     t.Status,
		"cell_id":    t.CellID.String(),
		"created_at": t.CreatedAt,
		"updated_at": t.UpdatedAt,
	}
	for _, key := range app.TenantSettingKeys() {
		if v, ok := t.Settings[key]; ok {
			res[key] = v
		}
	}
	return res
}
