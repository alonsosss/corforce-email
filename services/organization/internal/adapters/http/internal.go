package http

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/services/organization/internal/app"
	"github.com/alonsosss/corforce-email/services/organization/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// InternalHandler sirve en /internal/organization lo que otros servicios necesitan del
// registro de empresas sin leer sus tablas. Hoy, la celda de una empresa: el gateway la
// pregunta para llevar cada peticion con sesion a la instancia de los servicios de celda de
// esa celda. Solo lo llaman servicios: el router exige el token interno y
// RequireInternalCaller rechaza cualquier peticion que traiga usuario.
type InternalHandler struct {
	uc *app.OrganizationUseCase
}

func NewInternalHandler(uc *app.OrganizationUseCase) *InternalHandler {
	return &InternalHandler{uc: uc}
}

func (h *InternalHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.RequireInternalCaller)
	r.Get("/tenants/{tenantID}/cell", h.TenantCell)
	return r
}

// TenantCell responde 200 {tenant_id, cell_code} o 404 TENANT_NOT_FOUND. Solo el codigo de la
// celda: el host de su base es enrutado de datos del plano de control y no sale de aqui.
func (h *InternalHandler) TenantCell(w http.ResponseWriter, r *http.Request) {
	tenantID, err := uuid.Parse(chi.URLParam(r, "tenantID"))
	if err != nil {
		response.ErrBadRequest(w, "identificador de empresa no valido")
		return
	}
	cell, err := h.uc.TenantCell(r.Context(), tenantID)
	switch {
	case errors.Is(err, domain.ErrTenantNotFound):
		response.Err(w, http.StatusNotFound, "TENANT_NOT_FOUND", "empresa no encontrada")
	case err != nil:
		response.Unexpected(w, fmt.Errorf("celda de la empresa: %w", err))
	default:
		response.JSON(w, http.StatusOK, map[string]string{"tenant_id": tenantID.String(), "cell_code": cell.Code})
	}
}
