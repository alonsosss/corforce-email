package http

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/domain-service/internal/app"
	"github.com/alonsosss/corforce-email/services/domain-service/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// Permisos que exige cada operacion (modulo domains, recurso domains). El gateway ya
// gateo el modulo; aqui va la accion concreta, tercera capa.
const (
	permModule   = "domains"
	permResource = "domains"
)

// Authorizer es la tercera capa de control de acceso (pkg/authz.Checker).
type Authorizer interface {
	RequirePermission(module, resource, action string) func(http.Handler) http.Handler
}

// maxBodyBytes acota los cuerpos: ninguna peticion de este servicio lleva mas que un
// nombre de dominio y dos enumerados.
const maxBodyBytes = 4 * 1024

type Handler struct {
	uc    *app.UseCase
	authz Authorizer
}

func NewHandler(uc *app.UseCase, authz Authorizer) *Handler {
	return &Handler{uc: uc, authz: authz}
}

func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Route("/api/v1/domains", func(r chi.Router) {
		r.With(h.require("read")).Get("/", h.List)
		r.With(h.require("read")).Get("/{id}", h.Get)
		r.With(h.require("create")).Post("/", h.Create)
		r.With(h.require("update")).Patch("/{id}", h.Update)
		r.With(h.require("delete")).Delete("/{id}", h.Delete)
		r.With(h.require("verify")).Post("/{id}/verify", h.Verify)
		r.With(h.require("rotate_dkim")).Post("/{id}/rotate-dkim", h.RotateDKIM)
	})
	return r
}

func (h *Handler) require(action string) func(http.Handler) http.Handler {
	return h.authz.RequirePermission(permModule, permResource, action)
}

func tenantFromRequest(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	tenantID, err := uuid.Parse(middleware.GetTenantID(r.Context()))
	if err != nil {
		response.ErrUnauthorized(w, "la sesion no lleva empresa")
		return uuid.Nil, false
	}
	return tenantID, true
}

func parseIDParam(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.ErrBadRequest(w, "identificador de dominio no valido")
		return uuid.Nil, false
	}
	return id, true
}

// writeError traduce los errores de dominio; el resto se registra y responde 500.
func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrDomainNotFound):
		response.ErrNotFound(w, err.Error())
	case errors.Is(err, domain.ErrDomainAlreadyExists), errors.Is(err, domain.ErrDomainHasMailboxes):
		response.ErrConflict(w, err.Error())
	case errors.Is(err, domain.ErrInvalidDomainName), errors.Is(err, domain.ErrPlatformDomain),
		errors.Is(err, domain.ErrInvalidPurpose), errors.Is(err, domain.ErrInvalidDMARCPolicy),
		errors.Is(err, domain.ErrNothingToUpdate):
		response.ErrValidation(w, err.Error())
	case errors.Is(err, domain.ErrIntegrationUnavailable):
		response.Err(w, http.StatusServiceUnavailable, "INTEGRATION_UNAVAILABLE", domain.ErrIntegrationUnavailable.Error())
	default:
		response.Unexpected(w, err)
	}
}

// ── Peticiones ───────────────────────────────────────────────────────────────

type createRequest struct {
	Domain      string `json:"domain"`
	Purpose     string `json:"purpose"`
	DMARCPolicy string `json:"dmarc_policy,omitempty"`
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFromRequest(w, r)
	if !ok {
		return
	}
	var req createRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxBodyBytes); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	v := validate.New()
	v.Required("domain", req.Domain)
	v.MaxLength("domain", req.Domain, 253)
	v.Required("purpose", req.Purpose)
	v.OneOf("purpose", req.Purpose, domain.Purposes())
	v.OneOf("dmarc_policy", req.DMARCPolicy, domain.DMARCPolicies())
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	d, err := h.uc.Create(r.Context(), tenantID, app.CreateRequest{
		Domain: req.Domain, Purpose: req.Purpose, DMARCPolicy: req.DMARCPolicy,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, h.domainWithRecords(d))
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFromRequest(w, r)
	if !ok {
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	page, perPage = app.NormalizePage(page, perPage)
	domains, total, err := h.uc.List(r.Context(), tenantID, page, perPage)
	if err != nil {
		response.Unexpected(w, err)
		return
	}
	items := make([]map[string]interface{}, 0, len(domains))
	for _, d := range domains {
		items = append(items, domainResponse(d))
	}
	response.JSONWithMeta(w, http.StatusOK, items, response.PageMeta(total, page, perPage))
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFromRequest(w, r)
	if !ok {
		return
	}
	id, ok := parseIDParam(w, r)
	if !ok {
		return
	}
	d, err := h.uc.Get(r.Context(), tenantID, id)
	if err != nil {
		writeError(w, err)
		return
	}
	checks, err := h.uc.LatestChecks(r.Context(), tenantID, id)
	if err != nil {
		response.Unexpected(w, err)
		return
	}
	res := h.domainWithRecords(d)
	res["dns_checks"] = checksResponse(checks)
	response.JSON(w, http.StatusOK, res)
}

type updateRequest struct {
	Purpose     *string `json:"purpose,omitempty"`
	DMARCPolicy *string `json:"dmarc_policy,omitempty"`
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFromRequest(w, r)
	if !ok {
		return
	}
	id, ok := parseIDParam(w, r)
	if !ok {
		return
	}
	var req updateRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxBodyBytes); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	v := validate.New()
	if req.Purpose != nil {
		v.Required("purpose", *req.Purpose)
		v.OneOf("purpose", *req.Purpose, domain.Purposes())
	}
	if req.DMARCPolicy != nil {
		v.Required("dmarc_policy", *req.DMARCPolicy)
		v.OneOf("dmarc_policy", *req.DMARCPolicy, domain.DMARCPolicies())
	}
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	d, err := h.uc.Update(r.Context(), tenantID, id, app.UpdateRequest{Purpose: req.Purpose, DMARCPolicy: req.DMARCPolicy})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, h.domainWithRecords(d))
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFromRequest(w, r)
	if !ok {
		return
	}
	id, ok := parseIDParam(w, r)
	if !ok {
		return
	}
	if err := h.uc.Delete(r.Context(), tenantID, id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Verify responde 200 con el resultado sea cual sea: verificar es una consulta y que
// falte un registro es informacion, no un error de la peticion.
func (h *Handler) Verify(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFromRequest(w, r)
	if !ok {
		return
	}
	id, ok := parseIDParam(w, r)
	if !ok {
		return
	}
	res, err := h.uc.Verify(r.Context(), tenantID, id)
	if err != nil {
		writeError(w, err)
		return
	}
	out := h.domainWithRecords(res.Domain)
	out["outcome"] = string(res.Outcome)
	out["dns_checks"] = checksResponse(res.Checks)
	out["integration_errors"] = nilSafe(res.IntegrationErrors)
	response.JSON(w, http.StatusOK, out)
}

func (h *Handler) RotateDKIM(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFromRequest(w, r)
	if !ok {
		return
	}
	id, ok := parseIDParam(w, r)
	if !ok {
		return
	}
	res, err := h.uc.RotateDKIM(r.Context(), tenantID, id)
	if err != nil {
		writeError(w, err)
		return
	}
	out := domainResponse(res.Domain)
	out["dns_record"] = res.Record
	out["grace_until"] = res.GraceUntil
	response.JSON(w, http.StatusOK, out)
}

// ── Respuestas ───────────────────────────────────────────────────────────────

// domainResponse expone el dominio sin material privado: las claves cifradas nunca
// salen, y la publica solo como valor del TXT que el cliente debe publicar.
func domainResponse(d *domain.Domain) map[string]interface{} {
	res := map[string]interface{}{
		"id":                     d.ID.String(),
		"domain":                 d.Domain,
		"purpose":                string(d.Purpose),
		"status":                 string(d.Status),
		"verification_token":     d.VerificationToken,
		"verified_at":            d.VerifiedAt,
		"last_checked_at":        d.LastCheckedAt,
		"dkim_selector":          d.DKIMSelector,
		"dkim_public_key":        d.DKIMPublicKey,
		"dkim_key_bits":          d.DKIMKeyBits,
		"dkim_previous_selector": nilSafeString(d.DKIMPreviousSelector),
		"dkim_rotated_at":        d.DKIMRotatedAt,
		"dmarc_policy":           string(d.DMARCPolicy),
		"created_at":             d.CreatedAt,
		"updated_at":             d.UpdatedAt,
	}
	return res
}

func (h *Handler) domainWithRecords(d *domain.Domain) map[string]interface{} {
	res := domainResponse(d)
	res["dns_records"] = h.uc.ExpectedRecords(d)
	return res
}

func checksResponse(checks []domain.DNSCheck) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(checks))
	for _, c := range checks {
		out = append(out, map[string]interface{}{
			"record":     string(c.Record),
			"checked_at": c.CheckedAt,
			"expected":   c.Expected,
			"observed":   c.Observed,
			"ok":         c.OK,
			"detail":     c.Detail,
		})
	}
	return out
}

func nilSafe(items []string) []string {
	if items == nil {
		return []string{}
	}
	return items
}

func nilSafeString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
