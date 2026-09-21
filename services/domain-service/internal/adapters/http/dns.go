package http

import (
	"errors"
	"net/http"
	"strings"

	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/domain-service/internal/app"
	"github.com/alonsosss/corforce-email/services/domain-service/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// Publicacion automatica del DNS. El token de API solo entra en la peticion de conectar y no sale
// en ninguna respuesta ni en ningun error: la conexion se describe con metadatos (pista de cuatro
// caracteres, zonas, quien y cuando).

// Codigos propios. Ninguno es 401 ni 403: son del proveedor, no de la sesion del usuario, y el
// cliente web trata esos estados como sesion caducada o permiso denegado.
var dnsErrors = []struct {
	err    error
	status int
	code   string
}{
	{domain.ErrUnsupportedDNSProvider, http.StatusNotFound, "DNS_PROVIDER_UNSUPPORTED"},
	{domain.ErrInvalidDNSMode, http.StatusUnprocessableEntity, "VALIDATION_ERROR"},
	{domain.ErrInvalidRecordKind, http.StatusUnprocessableEntity, "VALIDATION_ERROR"},
	{domain.ErrInvalidDNSProviderToken, http.StatusUnprocessableEntity, "DNS_PROVIDER_TOKEN_INVALID"},
	{domain.ErrDNSProviderNotConnected, http.StatusConflict, "DNS_PROVIDER_NOT_CONNECTED"},
	{domain.ErrDNSModeManual, http.StatusConflict, "DNS_MODE_MANUAL"},
	{domain.ErrDNSProviderTokenInvalid, http.StatusUnprocessableEntity, "DNS_PROVIDER_TOKEN_INVALID"},
	{domain.ErrDNSProviderPermissionDenied, http.StatusUnprocessableEntity, "DNS_PROVIDER_PERMISSION_DENIED"},
	{domain.ErrDNSProviderNoZones, http.StatusUnprocessableEntity, "DNS_PROVIDER_NO_ZONES"},
	{domain.ErrDNSZoneNotFound, http.StatusConflict, "DNS_ZONE_NOT_FOUND"},
	{domain.ErrDNSProviderRateLimited, http.StatusTooManyRequests, "DNS_PROVIDER_RATE_LIMITED"},
	{domain.ErrDNSProviderUnavailable, http.StatusServiceUnavailable, "DNS_PROVIDER_UNAVAILABLE"},
	{domain.ErrDNSProviderRejected, http.StatusUnprocessableEntity, "DNS_PROVIDER_REJECTED"},
	{domain.ErrDNSRecordExists, http.StatusConflict, "DNS_RECORD_CONFLICT"},
}

// dnsErrorCode es el codigo propio de un error de la publicacion automatica, o "".
func dnsErrorCode(err error) string {
	for _, e := range dnsErrors {
		if errors.Is(err, e.err) {
			return e.code
		}
	}
	return ""
}

// writeDNSError responde con el mensaje del error de domain, nunca con la cadena envuelta, que
// puede llevar la ruta o los codigos de la llamada al proveedor.
func writeDNSError(w http.ResponseWriter, err error) {
	for _, e := range dnsErrors {
		if errors.Is(err, e.err) {
			response.Err(w, e.status, e.code, e.err.Error())
			return
		}
	}
	response.Unexpected(w, err)
}

func dnsMode(d *domain.Domain) string {
	if d.DNSMode == "" {
		return string(domain.DNSModeManual)
	}
	return string(d.DNSMode)
}

// maxTokenBodyBytes acota la peticion de conectar: un token y poco mas.
const maxTokenBodyBytes = 2 * 1024

type connectRequest struct {
	APIToken string `json:"api_token"`
}

func (h *Handler) DNSProviderStatus(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFromRequest(w, r)
	if !ok {
		return
	}
	provider := chi.URLParam(r, "provider")
	conn, err := h.uc.DNSProviderStatus(r.Context(), tenantID, provider)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, providerResponse(provider, conn))
}

func (h *Handler) ConnectDNSProvider(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFromRequest(w, r)
	if !ok {
		return
	}
	actor, ok := actorFromRequest(w, r)
	if !ok {
		return
	}
	var req connectRequest
	// El detalle de un JSON mal formado puede citar el cuerpo, que lleva el token: no se devuelve.
	if err := validate.DecodeJSONLimit(w, r, &req, maxTokenBodyBytes); err != nil {
		response.ErrBadRequest(w, "el cuerpo debe ser un objeto JSON con api_token")
		return
	}
	if strings.TrimSpace(req.APIToken) == "" {
		response.ErrValidation(w, "api_token es obligatorio")
		return
	}
	provider := chi.URLParam(r, "provider")
	conn, err := h.uc.ConnectDNSProvider(r.Context(), tenantID, actor, provider, req.APIToken)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, providerResponse(provider, conn))
}

func (h *Handler) DisconnectDNSProvider(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFromRequest(w, r)
	if !ok {
		return
	}
	actor, ok := actorFromRequest(w, r)
	if !ok {
		return
	}
	provider := chi.URLParam(r, "provider")
	res, err := h.uc.DisconnectDNSProvider(r.Context(), tenantID, actor, provider)
	if err != nil {
		writeError(w, err)
		return
	}
	out := providerResponse(provider, nil)
	out["disconnected"] = res.Disconnected
	out["domains_reset"] = res.DomainsReset
	response.JSON(w, http.StatusOK, out)
}

type dnsModeRequest struct {
	Mode string `json:"mode"`
}

func (h *Handler) SetDNSMode(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFromRequest(w, r)
	if !ok {
		return
	}
	id, ok := parseIDParam(w, r)
	if !ok {
		return
	}
	var req dnsModeRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxBodyBytes); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	v := validate.New()
	v.Required("mode", req.Mode)
	v.OneOf("mode", req.Mode, domain.DNSModes())
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	d, err := h.uc.SetDNSMode(r.Context(), tenantID, id, req.Mode)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, h.domainWithRecords(r.Context(), d))
}

type publishRequest struct {
	// Replace son los tipos de registro (ownership_txt, mx, spf, dkim, dkim_previous, dmarc) cuyos
	// registros del cliente en conflicto se confirma reemplazar.
	Replace []string `json:"replace,omitempty"`
}

// PublishDNS responde 200 con lo que hizo con cada registro, tambien si alguno quedo en conflicto o
// fallo: es informacion para decidir, no un error de la peticion. Un fallo que corta la publicacion
// (token, limite, zona) responde con su codigo.
func (h *Handler) PublishDNS(w http.ResponseWriter, r *http.Request) {
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
	var req publishRequest
	if r.ContentLength != 0 {
		if err := validate.DecodeJSONLimit(w, r, &req, maxBodyBytes); err != nil {
			response.ErrBadRequest(w, err.Error())
			return
		}
	}
	res, err := h.uc.PublishDNS(r.Context(), tenantID, id, actor, req.Replace)
	if err != nil {
		writeError(w, err)
		return
	}
	out := h.domainWithRecords(r.Context(), res.Domain)
	out["dns_publication"] = publicationResponse(res.Publication)
	if res.Verification != nil {
		out["outcome"] = string(res.Verification.Outcome)
		out["dns_checks"] = checksResponse(res.Verification.Checks)
		out["integration_errors"] = nilSafe(res.Verification.IntegrationErrors)
	} else {
		out["outcome"] = nil
		out["dns_checks"] = []map[string]interface{}{}
		out["integration_errors"] = []string{"verificar el dominio: no se pudo completar; vuelva a verificar"}
	}
	response.JSON(w, http.StatusOK, out)
}

// providerResponse describe la conexion sin el token: ni cifrado ni en claro.
func providerResponse(provider string, c *domain.DNSProviderConnection) map[string]interface{} {
	out := map[string]interface{}{"provider": provider, "connected": c != nil}
	if c == nil {
		return out
	}
	zones := c.Zones
	if zones == nil {
		zones = []string{}
	}
	var connectedBy interface{}
	if c.ConnectedBy != uuid.Nil {
		connectedBy = c.ConnectedBy.String()
	}
	out["token_hint"] = c.TokenHint
	out["zones"] = zones
	out["zones_visible"] = c.ZonesVisible
	out["connected_by"] = connectedBy
	out["connected_at"] = c.ConnectedAt
	out["last_validated_at"] = c.LastValidatedAt
	return out
}

func publicationResponse(p *domain.DNSPublication) map[string]interface{} {
	records := make([]map[string]interface{}, 0, len(p.Records))
	for _, rec := range p.Records {
		existing := rec.Existing
		if existing == nil {
			existing = []string{}
		}
		value := rec.Content
		if rec.Type == "MX" {
			value = domain.ExistingValue(domain.ProviderRecord{Type: rec.Type, Content: rec.Content, Priority: rec.Priority})
		}
		item := map[string]interface{}{
			"record":   string(rec.Kind),
			"type":     rec.Type,
			"host":     rec.Name,
			"value":    value,
			"action":   string(rec.Action),
			"existing": existing,
		}
		if rec.Err != nil {
			item["error_code"] = dnsErrorCode(rec.Err)
		}
		records = append(records, item)
	}
	return map[string]interface{}{
		"provider":     string(p.Provider),
		"zone":         p.Zone,
		"published_at": p.PublishedAt,
		"complete":     p.Complete(),
		"records":      records,
		"removed":      nilSafe(p.Removed),
		"kept":         nilSafe(p.Kept),
	}
}

// automationResponse es lo que hizo la publicacion automatica tras rotar o revocar claves DKIM.
func automationResponse(res *app.DNSAutomationResult) map[string]interface{} {
	out := map[string]interface{}{"provider": string(res.Provider), "publication": nil, "error_code": nil}
	if res.Publication != nil {
		out["publication"] = publicationResponse(res.Publication)
	}
	if res.Err != nil {
		code := dnsErrorCode(res.Err)
		if code == "" {
			code = "DNS_PROVIDER_UNAVAILABLE"
		}
		out["error_code"] = code
	}
	return out
}
