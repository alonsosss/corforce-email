package http

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/access-control/internal/app"
	"github.com/alonsosss/corforce-email/services/access-control/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

const (
	maxAPIKeyBody     = 16 << 10
	maxResolveBody    = 4 << 10
	apiKeysResource   = "api_keys"
	apiKeyInvalidCode = "API_KEY_INVALID"
)

// SMTPSettings es la direccion publica del relay SMTP que la pagina de claves ensena a la empresa.
// Vacia (Host == "") si la plataforma no ofrece SMTP.
type SMTPSettings struct {
	Host         string `json:"host"`
	StartTLSPort int    `json:"starttls_port"`
	TLSPort      int    `json:"tls_port"`
}

// APIKeysHandler sirve /api/v1/access/api-keys (por el gateway, con sesion de una persona) y la
// resolucion interna de claves que piden el gateway y smtp-relay.
type APIKeysHandler struct {
	uc     *app.APIKeysUseCase
	rbac   *app.RBACUseCase
	stepUp func(http.Handler) http.Handler
	smtp   SMTPSettings
}

func NewAPIKeysHandler(uc *app.APIKeysUseCase, rbac *app.RBACUseCase, stepUp func(http.Handler) http.Handler, smtp SMTPSettings) *APIKeysHandler {
	if stepUp == nil {
		stepUp = func(next http.Handler) http.Handler { return next }
	}
	return &APIKeysHandler{uc: uc, rbac: rbac, stepUp: stepUp, smtp: smtp}
}

// Routes se monta en /api/v1/access/api-keys.
func (h *APIKeysHandler) Routes() chi.Router {
	perms := &Handler{rbac: h.rbac}
	r := chi.NewRouter()
	r.Use(rejectAPIKeyCaller)
	r.With(perms.perm(apiKeysResource, "read")).Get("/", h.List)
	r.With(perms.perm(apiKeysResource, "read")).Get("/settings", h.Settings)
	r.With(perms.perm(apiKeysResource, "create")).Get("/scopes", h.Scopes)
	r.With(perms.perm(apiKeysResource, "create"), h.stepUp).Post("/", h.Create)
	r.With(perms.perm(apiKeysResource, "revoke")).Post("/{id}/revoke", h.Revoke)
	return r
}

// InternalRoutes se monta en /internal/access-control/api-keys: solo otros servicios.
func (h *APIKeysHandler) InternalRoutes() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.RequireInternalCaller)
	r.Post("/resolve", h.Resolve)
	return r
}

// rejectAPIKeyCaller: una clave no gestiona claves. El gateway ya no la deja llegar aqui (la
// lista de rutas que admiten clave es cerrada); esto es la defensa en el propio servicio.
func rejectAPIKeyCaller(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if middleware.GetAPIKeyID(r.Context()) != "" {
			response.ErrForbidden(w, "una clave de API no puede gestionar claves")
			return
		}
		next.ServeHTTP(w, r)
	})
}

type scopeDTO struct {
	Module      string `json:"module"`
	Resource    string `json:"resource"`
	Action      string `json:"action"`
	Description string `json:"description,omitempty"`
}

type apiKeyDTO struct {
	ID         uuid.UUID  `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Scopes     []scopeDTO `json:"scopes"`
	Status     string     `json:"status"`
	CreatedBy  uuid.UUID  `json:"created_by"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
	RevokedBy  *uuid.UUID `json:"revoked_by"`
	LastUsedAt *time.Time `json:"last_used_at"`
	LastUsedIP string     `json:"last_used_ip"`
}

func scopesDTO(ps []domain.Permission) []scopeDTO {
	out := make([]scopeDTO, len(ps))
	for i, p := range ps {
		out[i] = scopeDTO{Module: p.Module, Resource: p.Resource, Action: p.Action, Description: p.Description}
	}
	return out
}

func toAPIKeyDTO(k *domain.APIKey, now time.Time) apiKeyDTO {
	return apiKeyDTO{
		ID: k.ID, Name: k.Name, Prefix: k.Prefix, Scopes: scopesDTO(k.Scopes), Status: k.Status(now),
		CreatedBy: k.CreatedBy, CreatedAt: k.CreatedAt, ExpiresAt: k.ExpiresAt, RevokedAt: k.RevokedAt,
		RevokedBy: k.RevokedBy, LastUsedAt: k.LastUsedAt, LastUsedIP: k.LastUsedIP,
	}
}

func (h *APIKeysHandler) List(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := requestTenant(w, r)
	if !ok {
		return
	}
	keys, err := h.uc.List(r.Context(), tenantID)
	if err != nil {
		response.Unexpected(w, fmt.Errorf("listar las claves de API: %w", err))
		return
	}
	now := time.Now()
	out := make([]apiKeyDTO, len(keys))
	for i, k := range keys {
		out[i] = toAPIKeyDTO(k, now)
	}
	response.JSON(w, http.StatusOK, out)
}

func (h *APIKeysHandler) Settings(w http.ResponseWriter, r *http.Request) {
	var smtp *SMTPSettings
	if h.smtp.Host != "" {
		s := h.smtp
		smtp = &s
	}
	response.JSON(w, http.StatusOK, map[string]any{"smtp": smtp})
}

func (h *APIKeysHandler) Scopes(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := requestTenant(w, r)
	if !ok {
		return
	}
	actor, ok := requestActor(w, r)
	if !ok {
		return
	}
	perms, err := h.uc.GrantableFor(r.Context(), actor, tenantID)
	if err != nil {
		response.Unexpected(w, fmt.Errorf("leer los permisos que admite una clave: %w", err))
		return
	}
	response.JSON(w, http.StatusOK, scopesDTO(perms))
}

type createAPIKeyRequest struct {
	Name      string     `json:"name"`
	Scopes    []scopeDTO `json:"scopes"`
	ExpiresAt *time.Time `json:"expires_at"`
}

func (h *APIKeysHandler) Create(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := requestTenant(w, r)
	if !ok {
		return
	}
	actor, ok := requestActor(w, r)
	if !ok {
		return
	}
	var req createAPIKeyRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxAPIKeyBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	refs := make([]app.ScopeRef, len(req.Scopes))
	for i, s := range req.Scopes {
		refs[i] = app.ScopeRef{Module: s.Module, Resource: s.Resource, Action: s.Action}
	}
	created, err := h.uc.Create(r.Context(), app.CreateAPIKeyCommand{
		TenantID: tenantID, Actor: actor, Name: req.Name, Scopes: refs, ExpiresAt: req.ExpiresAt,
	})
	if err != nil {
		writeAPIKeyError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	response.JSON(w, http.StatusCreated, map[string]any{
		"key":    toAPIKeyDTO(created.Key, time.Now()),
		"secret": created.Token,
	})
}

func (h *APIKeysHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := requestTenant(w, r)
	if !ok {
		return
	}
	actor, ok := requestActor(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.ErrValidation(w, "id must be a valid UUID")
		return
	}
	key, err := h.uc.Revoke(r.Context(), tenantID, actor, id)
	if err != nil {
		writeAPIKeyError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, toAPIKeyDTO(key, time.Now()))
}

func writeAPIKeyError(w http.ResponseWriter, err error) {
	var validation *app.APIKeyValidationError
	switch {
	case errors.As(err, &validation):
		response.ErrValidation(w, validation.Error())
	case errors.Is(err, domain.ErrAPIKeyScopeNotGrantable):
		response.Err(w, http.StatusUnprocessableEntity, "SCOPE_NOT_GRANTABLE", "uno de los permisos no se puede dar a una clave de API")
	case errors.Is(err, domain.ErrPermissionNotHeld):
		response.ErrForbidden(w, "no puedes dar a una clave un permiso que no tienes")
	case errors.Is(err, domain.ErrAPIKeyLimit):
		response.Err(w, http.StatusConflict, "API_KEY_LIMIT", fmt.Sprintf("la empresa ya tiene %d claves vigentes", domain.MaxAPIKeysPerTenant))
	case errors.Is(err, domain.ErrAPIKeyNotFound):
		response.ErrNotFound(w, "api key not found")
	case errors.Is(err, domain.ErrAPIKeyRevoked):
		response.Err(w, http.StatusConflict, "API_KEY_REVOKED", "la clave ya estaba revocada")
	default:
		response.Unexpected(w, fmt.Errorf("claves de API: %w", err))
	}
}

type resolveRequest struct {
	Token    string `json:"token"`
	ClientIP string `json:"client_ip"`
}

type resolvedDTO struct {
	ID        uuid.UUID  `json:"id"`
	TenantID  uuid.UUID  `json:"tenant_id"`
	Kind      string     `json:"kind"`
	Prefix    string     `json:"prefix"`
	Scopes    []scopeDTO `json:"scopes"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// Resolve responde 200 con la clave, 401 API_KEY_INVALID sin distinguir el motivo y 503 si no
// se pudo comprobar. El token nunca aparece en el registro.
func (h *APIKeysHandler) Resolve(w http.ResponseWriter, r *http.Request) {
	var req resolveRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxResolveBody); err != nil {
		response.ErrBadRequest(w, "invalid body")
		return
	}
	ip := ""
	if parsed := net.ParseIP(req.ClientIP); parsed != nil {
		ip = parsed.String()
	}
	res, err := h.uc.Resolve(r.Context(), req.Token, ip)
	switch {
	case errors.Is(err, domain.ErrAPIKeyInvalid):
		response.Err(w, http.StatusUnauthorized, apiKeyInvalidCode, "api key invalid")
		return
	case err != nil:
		response.Err(w, http.StatusServiceUnavailable, "API_KEY_UNAVAILABLE", "no se pudo comprobar la clave")
		return
	}
	out := resolvedDTO{ID: res.ID, TenantID: res.TenantID, Kind: res.Kind, Prefix: res.Prefix, Scopes: make([]scopeDTO, len(res.Scopes)), ExpiresAt: res.ExpiresAt}
	for i, p := range res.Scopes {
		out.Scopes[i] = scopeDTO{Module: p.Module, Resource: p.Resource, Action: p.Action}
	}
	response.JSON(w, http.StatusOK, out)
}
