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

// Codigos de respuesta del indice de dominios. domain-service y el gateway (pkg/tenantcell)
// distinguen por ellos la respuesta definitiva de un fallo.
const (
	codeMailDomainNotFound = "MAIL_DOMAIN_NOT_FOUND"
	codeMailDomainClaimed  = "MAIL_DOMAIN_CLAIMED"
	codeInvalidMailDomain  = "INVALID_MAIL_DOMAIN"
	codeTenantBeingRemoved = "TENANT_BEING_REMOVED"
)

// InternalHandler sirve en /internal/organization lo que otros servicios necesitan del
// registro de empresas sin leer sus tablas: la celda de una empresa y la de un dominio de
// correo activo, que pregunta el gateway para llevar cada peticion a la instancia de los
// servicios de celda de esa celda, y el indice global de dominios activos, que escribe
// domain-service al activar y retirar dominios. Solo lo llaman servicios: el router exige el
// token interno y RequireInternalCaller rechaza cualquier peticion que traiga usuario.
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
	r.Put("/tenants/{tenantID}/mail-domains/{domain}", h.ClaimMailDomain)
	r.Delete("/tenants/{tenantID}/mail-domains/{domain}", h.ReleaseMailDomain)
	r.Get("/mail-domains/{domain}/cell", h.MailDomainCell)
	return r
}

// TenantCell responde 200 {tenant_id, cell_code} o 404 TENANT_NOT_FOUND. Solo el codigo de la
// celda: el host de su base es enrutado de datos del plano de control y no sale de aqui.
func (h *InternalHandler) TenantCell(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantParam(w, r)
	if !ok {
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

// ClaimMailDomain reclama el dominio para la empresa antes de activarlo: 200 {domain,
// tenant_id, cell_code} (tambien si ya era suyo), 409 MAIL_DOMAIN_CLAIMED si esta activo en otra
// empresa (sin decir cual), 409 TENANT_BEING_REMOVED si la empresa tiene la baja en curso, 404
// TENANT_NOT_FOUND o 422 INVALID_MAIL_DOMAIN.
func (h *InternalHandler) ClaimMailDomain(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantParam(w, r)
	if !ok {
		return
	}
	name, cell, err := h.uc.ClaimMailDomain(r.Context(), tenantID, chi.URLParam(r, "domain"))
	switch {
	case errors.Is(err, domain.ErrInvalidMailDomain):
		response.Err(w, http.StatusUnprocessableEntity, codeInvalidMailDomain, err.Error())
	case errors.Is(err, domain.ErrTenantNotFound):
		response.Err(w, http.StatusNotFound, "TENANT_NOT_FOUND", "empresa no encontrada")
	case errors.Is(err, domain.ErrMailDomainClaimed):
		response.Err(w, http.StatusConflict, codeMailDomainClaimed, err.Error())
	case errors.Is(err, domain.ErrTenantBeingRemoved):
		response.Err(w, http.StatusConflict, codeTenantBeingRemoved, err.Error())
	case err != nil:
		response.Unexpected(w, fmt.Errorf("reclamar dominio de correo: %w", err))
	default:
		response.JSON(w, http.StatusOK, map[string]string{"domain": name, "tenant_id": tenantID.String(), "cell_code": cell.Code})
	}
}

// ReleaseMailDomain retira el dominio de la empresa del indice: 204 aunque no estuviera o fuera
// de otra empresa, o 422 INVALID_MAIL_DOMAIN.
func (h *InternalHandler) ReleaseMailDomain(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantParam(w, r)
	if !ok {
		return
	}
	err := h.uc.ReleaseMailDomain(r.Context(), tenantID, chi.URLParam(r, "domain"))
	switch {
	case errors.Is(err, domain.ErrInvalidMailDomain):
		response.Err(w, http.StatusUnprocessableEntity, codeInvalidMailDomain, err.Error())
	case err != nil:
		response.Unexpected(w, fmt.Errorf("retirar dominio de correo: %w", err))
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// MailDomainCell responde 200 {domain, cell_code} o 404 MAIL_DOMAIN_NOT_FOUND, tambien para un
// nombre que no es un dominio. Ni la empresa ni el host de la base: el gateway solo necesita la
// celda.
func (h *InternalHandler) MailDomainCell(w http.ResponseWriter, r *http.Request) {
	name, cell, err := h.uc.MailDomainCell(r.Context(), chi.URLParam(r, "domain"))
	switch {
	case errors.Is(err, domain.ErrMailDomainNotFound):
		response.Err(w, http.StatusNotFound, codeMailDomainNotFound, "el dominio no está activo en ninguna celda")
	case err != nil:
		response.Unexpected(w, fmt.Errorf("celda del dominio de correo: %w", err))
	default:
		response.JSON(w, http.StatusOK, map[string]string{"domain": name, "cell_code": cell.Code})
	}
}

func tenantParam(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	tenantID, err := uuid.Parse(chi.URLParam(r, "tenantID"))
	if err != nil {
		response.ErrBadRequest(w, "identificador de empresa no válido")
		return uuid.Nil, false
	}
	return tenantID, true
}
