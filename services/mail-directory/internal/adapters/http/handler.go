package http

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/ports"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// Modulos de permiso: uno por prefijo del gateway (routes.json).
const (
	moduleDomains   = "domains"
	moduleMailboxes = "mailboxes"
	moduleRouting   = "mail_routing"

	actionRead        = "read"
	actionCreate      = "create"
	actionUpdate      = "update"
	actionDelete      = "delete"
	actionSetPassword = "set_password"

	// codeTenantRetired: la empresa esta dada de baja en la celda (domain.ErrTenantRetired).
	codeTenantRetired = "TENANT_RETIRED"
)

type Handler struct {
	uc    *app.UseCase
	authz *authz.Checker
}

func NewHandler(uc *app.UseCase, checker *authz.Checker) *Handler {
	return &Handler{uc: uc, authz: checker}
}

func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Route("/api/v1", func(r chi.Router) {
		r.Route("/mail-domains", h.domainRoutes)
		r.Route("/mailboxes", h.mailboxRoutes)
		r.Route("/mail-routing", h.routingRoutes)
		// Reglas del directorio para la interfaz: sin datos de la empresa, pero detras del
		// mismo permiso de lectura que el listado de buzones que las usa.
		r.With(h.require(moduleMailboxes, "mailboxes", actionRead)).Get("/mail-directory/meta", h.Meta)
	})
	// Ruta servicio-a-servicio: la protege RequireGatewayToken en main y toma la empresa
	// de X-Tenant-ID; no pasa por el gateway ni por permisos de usuario.
	r.Put("/internal/mail-directory/domains/{domain}/activation", h.SetDomainActivation)
	// domain-service anuncia con el id de esta politica el TXT _mta-sts del dominio; misma lectura que la
	// de la interfaz, con la empresa en X-Tenant-ID.
	r.Get("/internal/mail-directory/mta-sts/{domain}", h.GetMTASTS)
	// Politica MTA-STS que descargan otros servidores de correo: publica, sin sesion ni empresa.
	r.Get("/api/v1/public/mail-directory/mta-sts/{cell}/{domain}", h.PublicMTASTS)
	// La pide la saga de baja de organization, con la empresa en X-Tenant-ID. Solo servicios: una
	// peticion con usuario recibe 403.
	r.With(middleware.RequireInternalCaller).Put("/internal/mail-directory/tenant-retirement", h.RetireTenant)
	// La pide el webmail, que no conoce la empresa del buzon: no lleva X-Tenant-ID.
	r.Get("/internal/mail-directory/sender-identities", h.SenderIdentities)
	// La consulta de un buzon por id que hace mail-migration; solo servicios, con la empresa en X-Tenant-ID.
	r.With(middleware.RequireInternalCaller).Get("/internal/mail-directory/mailboxes/{id}", h.InternalMailbox)
	// La respuesta automatica del buzon con el que el webmail inicio sesion; tampoco lleva X-Tenant-ID.
	r.Get("/internal/mail-directory/vacation", h.InternalGetVacation)
	r.Put("/internal/mail-directory/vacation", h.InternalPutVacation)
	r.Get("/internal/mail-directory/directory", h.InternalSearchDirectory)
	return r
}

func (h *Handler) require(module, resource, action string) func(http.Handler) http.Handler {
	return h.authz.RequirePermission(module, resource, action)
}

// ── Helpers ──────────────────────────────────────────────────────────────────

func tenantFrom(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(middleware.GetTenantID(r.Context()))
	if err != nil {
		response.ErrUnauthorized(w, "la peticion no lleva empresa")
		return uuid.Nil, false
	}
	return id, true
}

// scopeFrom anade a la empresa si quien llama opera la plataforma. HasAnyRole sin roles
// adicionales solo deja pasar al superadmin.
func scopeFrom(w http.ResponseWriter, r *http.Request) (app.Scope, bool) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return app.Scope{}, false
	}
	return app.Scope{TenantID: tenantID, Platform: middleware.HasAnyRole(r.Context())}, true
}

func idParam(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		response.ErrBadRequest(w, "identificador no valido")
		return uuid.Nil, false
	}
	return id, true
}

func decode(w http.ResponseWriter, r *http.Request, dst interface{}) bool {
	if err := validate.DecodeJSON(r, dst); err != nil {
		response.ErrBadRequest(w, err.Error())
		return false
	}
	return true
}

func pageFrom(r *http.Request) (int, int, ports.Page) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	return app.NormalizePage(page, perPage)
}

// validationErrors son los fallos de entrada que se responden como 422.
var validationErrors = []error{
	domain.ErrInvalidDomainName, domain.ErrInvalidLocalPart, domain.ErrInvalidEmail, domain.ErrInvalidAddress,
	domain.ErrInvalidGoto, domain.ErrPasswordTooShort, domain.ErrPasswordTooLong, domain.ErrInvalidActive,
	domain.ErrInvalidKind, domain.ErrInvalidTLSPolicy, domain.ErrInvalidBCCType, domain.ErrInvalidHostname,
	domain.ErrInvalidLimit, domain.ErrSieveEmpty, domain.ErrSieveTooLarge, domain.ErrValidityRequired,
	domain.ErrNothingToUpdate, domain.ErrNameRequired, domain.ErrWildcardNeedsExtnl, domain.ErrDomainNotOwned,
	domain.ErrMailboxNotOwned, domain.ErrRelayhostNotOwned, domain.ErrActivationNotAllowed,
	domain.ErrQuotaExceedsMax, domain.ErrDomainQuotaExceeded, domain.ErrSearchTooLong,
	domain.ErrVacationMessageRequired, domain.ErrVacationMessageInvalid, domain.ErrVacationSubjectInvalid,
	domain.ErrVacationInterval, domain.ErrVacationWindow, domain.ErrVacationDate, domain.ErrInvalidMTASTSMode,
}

// conflictErrors son los choques con el estado actual: 409.
var conflictErrors = []error{
	domain.ErrAlreadyExists, domain.ErrDomainInUse, domain.ErrAddressTaken, domain.ErrMaxMailboxesReached,
	domain.ErrMaxAliasesReached, domain.ErrDomainIsOwnDomain, domain.ErrMTASTSTransition,
	domain.ErrMTASTSDomainNotActive, domain.ErrMTASTSMXMismatch,
}

func isAny(err error, list []error) bool {
	for _, candidate := range list {
		if errors.Is(err, candidate) {
			return true
		}
	}
	return false
}

func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		response.ErrNotFound(w, err.Error())
	case errors.Is(err, domain.ErrPlatformOnly):
		response.ErrForbidden(w, err.Error())
	case errors.Is(err, domain.ErrTenantRetired):
		response.Err(w, http.StatusConflict, codeTenantRetired, err.Error())
	case isAny(err, conflictErrors):
		response.ErrConflict(w, err.Error())
	case isAny(err, validationErrors):
		response.ErrValidation(w, err.Error())
	default:
		response.Unexpected(w, err)
	}
}

// listOf y getOf cubren los recursos cuyo listado y detalle solo dependen de la empresa.
func listOf[T any](list func(context.Context, uuid.UUID, ports.Page) ([]T, int64, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tenantID, ok := tenantFrom(w, r)
		if !ok {
			return
		}
		page, perPage, pg := pageFrom(r)
		items, total, err := list(r.Context(), tenantID, pg)
		if err != nil {
			writeError(w, err)
			return
		}
		response.JSONWithMeta(w, http.StatusOK, items, response.PageMeta(total, page, perPage))
	}
}

func getOf[T any](get func(context.Context, uuid.UUID, uuid.UUID) (*T, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tenantID, ok := tenantFrom(w, r)
		if !ok {
			return
		}
		id, ok := idParam(w, r, "id")
		if !ok {
			return
		}
		item, err := get(r.Context(), tenantID, id)
		if err != nil {
			writeError(w, err)
			return
		}
		response.JSON(w, http.StatusOK, item)
	}
}

func deleteOf(del func(context.Context, uuid.UUID, uuid.UUID) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tenantID, ok := tenantFrom(w, r)
		if !ok {
			return
		}
		id, ok := idParam(w, r, "id")
		if !ok {
			return
		}
		if err := del(r.Context(), tenantID, id); err != nil {
			writeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// ── Dominios ──────────────────────────────────────────────────────────────────

// Meta responde las reglas del directorio (app.DirectoryMeta).
func (h *Handler) Meta(w http.ResponseWriter, r *http.Request) {
	response.JSON(w, http.StatusOK, app.DirectoryMeta())
}

func (h *Handler) domainRoutes(r chi.Router) {
	const res, aliasRes = "domains", "alias_domains"
	r.Route("/mta-sts", h.mtaSTSRoutes)
	r.With(h.require(moduleDomains, res, actionRead)).Get("/", h.ListDomains)
	r.With(h.require(moduleDomains, res, actionCreate)).Post("/", h.CreateDomain)
	r.With(h.require(moduleDomains, aliasRes, actionRead)).Get("/alias-domains", listOf(h.uc.ListAliasDomains))
	r.With(h.require(moduleDomains, aliasRes, actionCreate)).Post("/alias-domains", h.CreateAliasDomain)
	r.With(h.require(moduleDomains, aliasRes, actionUpdate)).Patch("/alias-domains/{id}", h.UpdateAliasDomain)
	r.With(h.require(moduleDomains, aliasRes, actionDelete)).Delete("/alias-domains/{id}", deleteOf(h.uc.DeleteAliasDomain))
	r.With(h.require(moduleDomains, res, actionRead)).Get("/{id}", getOf(h.uc.GetDomain))
	r.With(h.require(moduleDomains, res, actionUpdate)).Patch("/{id}", h.UpdateDomain)
	r.With(h.require(moduleDomains, res, actionDelete)).Delete("/{id}", deleteOf(h.uc.DeleteDomain))
}

// ListDomains admite ?search= (subcadena del nombre, sin distinguir mayusculas) ademas
// de la paginacion.
func (h *Handler) ListDomains(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	page, perPage, pg := pageFrom(r)
	items, total, err := h.uc.ListDomains(r.Context(), tenantID, ports.DomainFilter{Search: r.URL.Query().Get("search")}, pg)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSONWithMeta(w, http.StatusOK, items, response.PageMeta(total, page, perPage))
}

func (h *Handler) CreateDomain(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	var req createDomainRequest
	if !decode(w, r, &req) {
		return
	}
	v := validate.New()
	v.Required("domain", req.Domain)
	v.MaxLength("description", req.Description, domain.MaxDescriptionLength)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	d, err := h.uc.CreateDomain(r.Context(), tenantID, app.CreateDomainRequest{
		Domain: req.Domain, Description: req.Description, BackupMX: req.BackupMX,
		RelayAllRecipients: req.RelayAllRecipients, RelayUnknownOnly: req.RelayUnknownOnly, RelayhostID: req.RelayhostID,
		Limits: domain.DomainLimits{
			MaxAliases: req.MaxAliases, MaxMailboxes: req.MaxMailboxes, DefaultQuotaBytes: req.DefaultQuotaBytes,
			MaxQuotaBytes: req.MaxQuotaBytes, QuotaBytes: req.QuotaBytes,
		},
	})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, d)
}

func (h *Handler) UpdateDomain(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	var req updateDomainRequest
	if !decode(w, r, &req) {
		return
	}
	if req.Description != nil {
		v := validate.New()
		v.MaxLength("description", *req.Description, domain.MaxDescriptionLength)
		if !v.Valid() {
			response.ErrValidation(w, v.Error())
			return
		}
	}
	d, err := h.uc.UpdateDomain(r.Context(), tenantID, id, app.UpdateDomainRequest{
		Description: req.Description, Active: req.Active, BackupMX: req.BackupMX,
		RelayAllRecipients: req.RelayAllRecipients, RelayUnknownOnly: req.RelayUnknownOnly,
		RelayhostID: req.RelayhostID.Value, ClearRelayhost: req.RelayhostID.Set && req.RelayhostID.Value == nil,
		MaxAliases: req.MaxAliases, MaxMailboxes: req.MaxMailboxes, DefaultQuotaBytes: req.DefaultQuotaBytes,
		MaxQuotaBytes: req.MaxQuotaBytes, QuotaBytes: req.QuotaBytes,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, d)
}

// SetDomainActivation la llama domain-service al verificar (o dar de baja) un dominio.
// Desactivar un dominio con buzones se rechaza con 409: primero se retiran.
func (h *Handler) SetDomainActivation(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	var req activationRequest
	if !decode(w, r, &req) {
		return
	}
	if req.Active == nil {
		response.ErrValidation(w, "active is required")
		return
	}
	d, err := h.uc.SetDomainActivation(r.Context(), tenantID, chi.URLParam(r, "domain"), *req.Active)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, d)
}

// RetireTenant da de baja a la empresa de X-Tenant-ID en el directorio de la celda
// (app.RetireTenant). Idempotente: 200 {tenant_id, retired_at, deactivated} con lo que apago esta
// llamada, todo a cero al repetirla.
func (h *Handler) RetireTenant(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	out, err := h.uc.RetireTenant(r.Context(), tenantID)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, out)
}

func (h *Handler) CreateAliasDomain(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	var req createAliasDomainRequest
	if !decode(w, r, &req) {
		return
	}
	v := validate.New()
	v.Required("alias_domain", req.AliasDomain)
	v.Required("target_domain", req.TargetDomain)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	a, err := h.uc.CreateAliasDomain(r.Context(), tenantID, app.CreateAliasDomainRequest{
		AliasDomain: req.AliasDomain, TargetDomain: req.TargetDomain, Active: req.Active,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, a)
}

func (h *Handler) UpdateAliasDomain(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	var req updateAliasDomainRequest
	if !decode(w, r, &req) {
		return
	}
	a, err := h.uc.UpdateAliasDomain(r.Context(), tenantID, id, app.UpdateAliasDomainRequest{
		TargetDomain: req.TargetDomain, Active: req.Active,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, a)
}
