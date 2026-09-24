package http

import (
	"context"
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
	permModule           = "domains"
	permResource         = "domains"
	permProviderResource = "dns_providers"
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
	// stepUp exige reconfirmar la identidad (middleware.RequireStepUp) para guardar una credencial
	// de terceros; nil no exige nada.
	stepUp func(http.Handler) http.Handler
}

func NewHandler(uc *app.UseCase, authz Authorizer, stepUp func(http.Handler) http.Handler) *Handler {
	if stepUp == nil {
		stepUp = func(next http.Handler) http.Handler { return next }
	}
	return &Handler{uc: uc, authz: authz, stepUp: stepUp}
}

func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Route("/api/v1/domains", func(r chi.Router) {
		// Conexion de la empresa con su proveedor DNS. Todas las escrituras van por POST para que el
		// gateway no exija update o delete del modulo a quien solo tiene estos permisos. El permiso va
		// antes que el step-up: no se pide reconfirmar la identidad a quien no puede conectar.
		r.Route("/dns-providers/{provider}", func(r chi.Router) {
			r.With(h.requireProvider("read")).Get("/", h.DNSProviderStatus)
			r.With(h.requireProvider("connect"), h.stepUp).Post("/connect", h.ConnectDNSProvider)
			r.With(h.requireProvider("disconnect")).Post("/disconnect", h.DisconnectDNSProvider)
		})
		r.With(h.require("read")).Get("/", h.List)
		r.With(h.require("read")).Get("/{id}", h.Get)
		r.With(h.require("create")).Post("/", h.Create)
		r.With(h.require("update")).Patch("/{id}", h.Update)
		r.With(h.require("delete")).Delete("/{id}", h.Delete)
		r.With(h.require("verify")).Post("/{id}/verify", h.Verify)
		r.With(h.require("rotate_dkim")).Post("/{id}/rotate-dkim", h.RotateDKIM)
		// Revocar corta la firma hasta que el cliente publique el TXT nuevo: accion aparte de la
		// rotacion programada (030_domain_service_dkim_revoke.sql).
		r.With(h.require("revoke_dkim")).Post("/{id}/revoke-dkim", h.RevokeDKIM)
		r.With(h.require("publish_dns")).Post("/{id}/dns-mode", h.SetDNSMode)
		r.With(h.require("publish_dns")).Post("/{id}/publish-dns", h.PublishDNS)
	})
	return r
}

func (h *Handler) require(action string) func(http.Handler) http.Handler {
	return h.authz.RequirePermission(permModule, permResource, action)
}

func (h *Handler) requireProvider(action string) func(http.Handler) http.Handler {
	return h.authz.RequirePermission(permModule, permProviderResource, action)
}

func tenantFromRequest(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	tenantID, err := uuid.Parse(middleware.GetTenantID(r.Context()))
	if err != nil {
		response.ErrUnauthorized(w, "la sesión no lleva empresa")
		return uuid.Nil, false
	}
	return tenantID, true
}

// actorFromRequest es el usuario que pide un cambio de claves: queda en el historial y en el
// evento, asi que una peticion sin usuario no cambia claves.
func actorFromRequest(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	actor, err := uuid.Parse(middleware.GetUserID(r.Context()))
	if err != nil || actor == uuid.Nil {
		response.ErrUnauthorized(w, "la sesión no lleva usuario")
		return uuid.Nil, false
	}
	return actor, true
}

func parseIDParam(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.ErrBadRequest(w, "identificador de dominio no válido")
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
	case errors.Is(err, domain.ErrDKIMRotationInProgress):
		response.Err(w, http.StatusConflict, "DKIM_ROTATION_IN_PROGRESS", err.Error())
	case errors.Is(err, domain.ErrDKIMSelectorNotCurrent):
		response.Err(w, http.StatusConflict, "DKIM_SELECTOR_NOT_CURRENT", err.Error())
	case errors.Is(err, domain.ErrDKIMKeysChanged):
		response.Err(w, http.StatusConflict, "DKIM_KEYS_CHANGED", err.Error())
	case errors.Is(err, domain.ErrInvalidDomainName), errors.Is(err, domain.ErrPlatformDomain),
		errors.Is(err, domain.ErrPublicSuffixDomain), errors.Is(err, domain.ErrInvalidPurpose), errors.Is(err, domain.ErrInvalidDMARCPolicy),
		errors.Is(err, domain.ErrNothingToUpdate), errors.Is(err, domain.ErrInvalidRevocationReason):
		response.ErrValidation(w, err.Error())
	case errors.Is(err, domain.ErrIntegrationUnavailable):
		response.Err(w, http.StatusServiceUnavailable, "INTEGRATION_UNAVAILABLE", domain.ErrIntegrationUnavailable.Error())
	case dnsErrorCode(err) != "":
		writeDNSError(w, err)
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
		PlatformOperator: middleware.IsSuperadmin(r.Context()),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, h.domainWithRecords(r.Context(), d))
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
		items = append(items, h.domainResponse(d))
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
	rotations, err := h.uc.DKIMRotations(r.Context(), tenantID, id)
	if err != nil {
		response.Unexpected(w, err)
		return
	}
	res := h.domainWithRecords(r.Context(), d)
	res["dns_checks"] = checksResponse(checks)
	res["dkim_rotations"] = rotationsResponse(rotations)
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
	response.JSON(w, http.StatusOK, h.domainWithRecords(r.Context(), d))
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
	out := h.domainWithRecords(r.Context(), res.Domain)
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
	actor, ok := actorFromRequest(w, r)
	if !ok {
		return
	}
	res, err := h.uc.RotateDKIM(r.Context(), tenantID, id, actor)
	if err != nil {
		writeError(w, err)
		return
	}
	out := h.domainResponse(res.Domain)
	out["dns_record"] = res.Record
	out["grace_until"] = res.GraceUntil
	if res.DNS != nil {
		out["dns_automation"] = automationResponse(res.DNS)
	}
	response.JSON(w, http.StatusOK, out)
}

type revokeRequest struct {
	CurrentSelector string `json:"current_selector"`
	Reason          string `json:"reason"`
}

// RevokeDKIM responde 200 en cuanto la revocacion queda guardada, tambien si la celda no la
// confirmo: el cliente necesita igual el TXT nuevo y los que debe retirar de su DNS.
// engines_retired dice si los motores ya no tienen ninguna clave revocada; si es false, la clave
// revocada puede seguir firmando hasta que el barrido o un reintento de la misma peticion lo
// confirmen (integration_errors lleva el motivo).
func (h *Handler) RevokeDKIM(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFromRequest(w, r)
	if !ok {
		return
	}
	id, ok := parseIDParam(w, r)
	if !ok {
		return
	}
	actor, ok := actorFromRequest(w, r)
	if !ok {
		return
	}
	var req revokeRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxBodyBytes); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	v := validate.New()
	v.Required("current_selector", req.CurrentSelector)
	v.MaxLength("current_selector", req.CurrentSelector, 63)
	v.Required("reason", req.Reason)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	res, err := h.uc.RevokeDKIM(r.Context(), tenantID, id, app.RevokeDKIMRequest{
		CurrentSelector: req.CurrentSelector, Reason: req.Reason, ActorID: actor,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	out := h.domainResponse(res.Domain)
	out["dns_record"] = res.Record
	out["remove_dns_records"] = res.RemoveRecords
	out["revocation"] = rotationResponse(res.Rotation)
	out["engines_retired"] = res.EnginesRetired
	out["integration_errors"] = nilSafe(res.IntegrationErrors)
	if res.DNS != nil {
		out["dns_automation"] = automationResponse(res.DNS)
	}
	response.JSON(w, http.StatusOK, out)
}

// ── Respuestas ───────────────────────────────────────────────────────────────

// domainResponse expone el dominio sin material privado: las claves cifradas nunca
// salen, y la publica solo como valor del TXT que el cliente debe publicar.
// dkim_previous_until es hasta cuando, como pronto, debe seguir publicado el TXT de la clave
// anterior; dkim_revocation_pending, que una revocacion sigue sin confirmar en la celda. ses_* es el
// estado de su identidad en Amazon SES, nulo si no la tiene.
func (h *Handler) domainResponse(d *domain.Domain) map[string]interface{} {
	res := map[string]interface{}{
		"id":                      d.ID.String(),
		"domain":                  d.Domain,
		"purpose":                 string(d.Purpose),
		"status":                  string(d.Status),
		"verification_token":      d.VerificationToken,
		"verified_at":             d.VerifiedAt,
		"last_checked_at":         d.LastCheckedAt,
		"dkim_selector":           d.DKIMSelector,
		"dkim_public_key":         d.DKIMPublicKey,
		"dkim_key_bits":           d.DKIMKeyBits,
		"dkim_previous_selector":  nilSafeString(d.DKIMPreviousSelector),
		"dkim_rotated_at":         d.DKIMRotatedAt,
		"dkim_previous_until":     h.uc.PreviousDKIMRetireAfter(d),
		"dkim_revocation_pending": d.DKIMRevocationPending,
		"dmarc_policy":            string(d.DMARCPolicy),
		"dns_mode":                dnsMode(d),
		"dns_published_at":        d.DNSPublishedAt,
		"ses_identity_status":     nilSafeString(string(d.SES.IdentityStatus)),
		"ses_dkim_status":         nilSafeString(string(d.SES.DKIMStatus)),
		"ses_mail_from_status":    nilSafeString(string(d.SES.MailFromStatus)),
		"ses_checked_at":          d.SES.CheckedAt,
		"ses_last_error":          nilSafeString(d.SES.LastError),
		"created_at":              d.CreatedAt,
		"updated_at":              d.UpdatedAt,
	}
	return res
}

// rotationResponse es una entrada del historial de claves. actor_id es nulo si no la pidio una
// persona.
func rotationResponse(r domain.DKIMRotation) map[string]interface{} {
	revoked := r.RevokedSelectors
	if revoked == nil {
		revoked = []string{}
	}
	var actor interface{}
	if r.ActorID != uuid.Nil {
		actor = r.ActorID.String()
	}
	return map[string]interface{}{
		"id":                r.ID.String(),
		"kind":              string(r.Kind),
		"selector":          r.Selector,
		"previous_selector": nilSafeString(r.PreviousSelector),
		"revoked_selectors": revoked,
		"reason":            r.Reason,
		"actor_id":          actor,
		"rotated_at":        r.RotatedAt,
	}
}

func rotationsResponse(rotations []domain.DKIMRotation) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(rotations))
	for _, r := range rotations {
		out = append(out, rotationResponse(r))
	}
	return out
}

func (h *Handler) domainWithRecords(ctx context.Context, d *domain.Domain) map[string]interface{} {
	res := h.domainResponse(d)
	res["dns_records"] = h.uc.ExpectedRecords(ctx, d)
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
