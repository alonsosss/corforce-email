package http

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/tenantcell"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// Modulo de permisos que gatea el gateway y que exigen los handlers.
const permissionModule = "mail_security"

// Tope del JSON de entrada del API de administracion (plantillas HTML incluidas).
const adminMaxBody = 1 << 20

// Handler sirve el API de administracion (tras el gateway) y las rutas internas
// servicio-a-servicio (tras el token interno).
type Handler struct {
	policy     *app.PolicyUseCase
	quarantine *app.QuarantineUseCase
	firewall   *app.FirewallUseCase
	dkim       *app.DKIMUseCase
	queue      *app.QueueUseCase
	antispam   *app.AntispamUseCase
	spamCheck  *app.SpamCheckUseCase
	authz      *authz.Checker
	// publicLimiter frena por ip los enlaces sin sesion del aviso de cuarentena, por
	// debajo del limite general del servicio.
	publicLimiter *middleware.RateLimiter
}

// publicLinksPerMinute es el cupo por ip de los enlaces sin sesion.
const publicLinksPerMinute = 30

func NewHandler(policy *app.PolicyUseCase, quarantine *app.QuarantineUseCase, firewall *app.FirewallUseCase, dkim *app.DKIMUseCase, checker *authz.Checker) *Handler {
	return &Handler{policy: policy, quarantine: quarantine, firewall: firewall, dkim: dkim, authz: checker,
		publicLimiter: middleware.NewRateLimiter(publicLinksPerMinute, time.Minute)}
}

// WithQueue conecta el gestor de la cola de Postfix. Sin el, las rutas de la cola responden 503
// NOT_CONFIGURED: el resto del servicio no depende de el.
func (h *Handler) WithQueue(queue *app.QueueUseCase) *Handler {
	h.queue = queue
	return h
}

// WithAntispam conecta la lectura del controller de Rspamd (estadisticas e historial). Sin ella, sus rutas
// responden 503 NOT_CONFIGURED.
func (h *Handler) WithAntispam(antispam *app.AntispamUseCase) *Handler {
	h.antispam = antispam
	return h
}

func (h *Handler) can(resource, action string) func(http.Handler) http.Handler {
	return h.authz.RequirePermission(permissionModule, resource, action)
}

// adminPrefix es el prefijo del API de administracion, el que enruta el gateway.
const adminPrefix = "/api/v1/mail-security"

// platformRoutes son las rutas de plataforma de la celda: el cortafuegos (mail_security/firewall/*), la
// cola de Postfix (mail_security/queue/*) y la lectura del antispam (mail_security/rspamd/read). El permiso
// es de plataforma y, en el caso de uso, se exige superadmin. Son las unicas que un operador con celda
// destino alcanza en una celda que no es la de su empresa (PlatformRoutes).
var platformRoutes = []struct {
	method, path, resource, action string
	serve                          func(*Handler, http.ResponseWriter, *http.Request)
}{
	{http.MethodGet, "/firewall/networks", "firewall", "read", (*Handler).ListFirewallNetworks},
	{http.MethodPost, "/firewall/networks", "firewall", "create", (*Handler).AddFirewallNetwork},
	{http.MethodDelete, "/firewall/networks/{id}", "firewall", "delete", (*Handler).DeleteFirewallNetwork},
	{http.MethodGet, "/firewall/options", "firewall", "read", (*Handler).GetFirewallOptions},
	{http.MethodPut, "/firewall/options", "firewall", "update", (*Handler).PutFirewallOptions},
	{http.MethodGet, "/firewall/bans", "firewall", "read", (*Handler).ListFirewallBans},
	{http.MethodPost, "/firewall/bans/unban", "firewall", "update", (*Handler).UnbanFirewallNetwork},

	{http.MethodGet, "/queue", "queue", "read", (*Handler).ListQueue},
	{http.MethodPost, "/queue/flush", "queue", "update", (*Handler).FlushQueue},
	{http.MethodPost, "/queue/{id}/retry", "queue", "update", queueAction(domain.QueueRetry)},
	{http.MethodPost, "/queue/{id}/hold", "queue", "update", queueAction(domain.QueueHold)},
	{http.MethodPost, "/queue/{id}/unhold", "queue", "update", queueAction(domain.QueueUnhold)},
	{http.MethodDelete, "/queue/{id}", "queue", "delete", queueAction(domain.QueueDelete)},

	{http.MethodGet, "/rspamd/stats", "rspamd", "read", (*Handler).RspamdStats},
	{http.MethodGet, "/rspamd/history", "rspamd", "read", (*Handler).RspamdHistory},
}

// PlatformRoutes son las rutas de plataforma tal como las monta Routes, para
// tenantcell.Membership.AcceptOperators.
func PlatformRoutes() []tenantcell.Route {
	out := make([]tenantcell.Route, 0, len(platformRoutes))
	for _, pr := range platformRoutes {
		out = append(out, tenantcell.Route{Method: pr.method, Pattern: adminPrefix + pr.path})
	}
	return out
}

func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Route(adminPrefix, func(r chi.Router) {
		r.Get("/health", h.Health)

		r.With(h.can("spam_scores", "read")).Get("/spam-scores", h.ListSpamScores)
		r.With(h.can("spam_scores", "read")).Get("/spam-scores/{object}", h.GetSpamScore)
		r.With(h.can("spam_scores", "update")).Put("/spam-scores/{object}", h.PutSpamScore)
		r.With(h.can("spam_scores", "delete")).Delete("/spam-scores/{object}", h.DeleteSpamScore)

		r.With(h.can("address_lists", "read")).Get("/address-lists", h.ListAddressLists)
		r.With(h.can("address_lists", "create")).Post("/address-lists", h.CreateAddressList)
		r.With(h.can("address_lists", "delete")).Delete("/address-lists/{id}", h.DeleteAddressList)

		r.With(h.can("settings_maps", "read")).Get("/settings-maps", h.ListSettingsMaps)
		r.With(h.can("settings_maps", "create")).Post("/settings-maps", h.CreateSettingsMap)
		r.With(h.can("settings_maps", "update")).Patch("/settings-maps/{id}", h.PatchSettingsMap)
		r.With(h.can("settings_maps", "delete")).Delete("/settings-maps/{id}", h.DeleteSettingsMap)

		r.With(h.can("footers", "read")).Get("/footers", h.ListFooters)
		r.With(h.can("footers", "read")).Get("/footers/{domain}", h.GetFooter)
		r.With(h.can("footers", "update")).Put("/footers/{domain}", h.PutFooter)
		r.With(h.can("footers", "delete")).Delete("/footers/{domain}", h.DeleteFooter)

		r.With(h.can("forwarding_hosts", "read")).Get("/forwarding-hosts", h.ListForwardingHosts)
		r.With(h.can("forwarding_hosts", "create")).Post("/forwarding-hosts", h.CreateForwardingHost)
		r.With(h.can("forwarding_hosts", "delete")).Delete("/forwarding-hosts/{id}", h.DeleteForwardingHost)

		r.With(h.can("rate_limits", "read")).Get("/rate-limits", h.ListRateLimits)
		r.With(h.can("rate_limits", "read")).Get("/rate-limits/{object}", h.GetRateLimit)
		r.With(h.can("rate_limits", "update")).Put("/rate-limits/{object}", h.PutRateLimit)
		r.With(h.can("rate_limits", "delete")).Delete("/rate-limits/{object}", h.DeleteRateLimit)

		r.With(h.can("mailbox_tags", "read")).Get("/mailbox-tags", h.ListMailboxTags)
		r.With(h.can("mailbox_tags", "read")).Get("/mailbox-tags/{username}", h.GetMailboxTags)
		r.With(h.can("mailbox_tags", "update")).Put("/mailbox-tags/{username}", h.PutMailboxTags)

		r.With(h.can("quarantine", "read")).Get("/quarantine", h.ListQuarantine)
		r.With(h.can("quarantine", "read")).Get("/quarantine/{id}", h.GetQuarantine)
		r.With(h.can("quarantine", "read")).Get("/quarantine/{id}/message", h.GetQuarantineMessage)
		r.With(h.can("quarantine", "delete")).Delete("/quarantine/{id}", h.DeleteQuarantine)
		r.With(h.can("quarantine", "release")).Post("/quarantine/{id}/release", h.ReleaseQuarantine)
		r.With(h.can("quarantine", "learn")).Post("/quarantine/{id}/learn-spam", h.LearnSpam)
		r.With(h.can("quarantine", "release"), h.can("quarantine", "learn")).Post("/quarantine/{id}/release-ham", h.ReleaseQuarantineAsHam)

		r.With(h.can("quarantine_settings", "read")).Get("/quarantine-settings", h.GetQuarantineSettings)
		r.With(h.can("quarantine_settings", "update")).Put("/quarantine-settings", h.PutQuarantineSettings)

		r.With(h.can("smtp_access", "read")).Get("/smtp-access", h.ListSMTPAccess)
		r.With(h.can("smtp_access", "read")).Get("/smtp-access/{username}", h.GetSMTPAccess)
		r.With(h.can("smtp_access", "update")).Put("/smtp-access/{username}", h.PutSMTPAccess)
		r.With(h.can("smtp_access", "delete")).Delete("/smtp-access/{username}", h.DeleteSMTPAccess)

		for _, pr := range platformRoutes {
			r.With(h.can(pr.resource, pr.action)).Method(pr.method, pr.path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				pr.serve(h, w, r)
			}))
		}
	})

	// Enlaces del aviso de cuarentena: publicos, los verifica la firma del enlace.
	r.Route("/api/v1/public/mail-security/quarantine", h.publicRoutes)

	// Rutas internas: las llaman domain-service (DKIM, con X-Tenant-ID) y templates (spam-check, sin
	// empresa) con el token de gateway. Sin permiso de usuario porque no hay usuario: la autoridad es el
	// servicio.
	r.Route("/internal/mail-security", func(r chi.Router) {
		r.Post("/spam-check", h.SpamCheck)
		r.Put("/dkim/{domain}", h.PutDKIM)
		r.Delete("/dkim/{domain}", h.DeleteDKIMDomain)
		r.Delete("/dkim/{domain}/{selector}", h.DeleteDKIMSelector)
	})
	return r
}

func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	response.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ── Utilidades ────────────────────────────────────────────────────────────────

func tenantFrom(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(middleware.GetTenantID(r.Context()))
	if err != nil {
		response.ErrUnauthorized(w, "invalid tenant")
		return uuid.Nil, false
	}
	return id, true
}

func idParam(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		response.ErrBadRequest(w, name+" invalido")
		return uuid.Nil, false
	}
	return id, true
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, adminMaxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		response.ErrBadRequest(w, "cuerpo JSON invalido: "+err.Error())
		return false
	}
	return true
}

func writeError(w http.ResponseWriter, err error) {
	var verr *domain.ValidationError
	switch {
	case errors.Is(err, domain.ErrNotFound):
		response.ErrNotFound(w, "no encontrado")
	case errors.Is(err, domain.ErrObjectNotOwned), errors.Is(err, domain.ErrPlatformOnly):
		response.ErrForbidden(w, err.Error())
	case errors.Is(err, domain.ErrAlreadyExists):
		response.ErrConflict(w, "ya existe")
	case errors.Is(err, domain.ErrDKIMDomainNotActive):
		response.Err(w, http.StatusConflict, "DKIM_DOMAIN_NOT_ACTIVE", err.Error())
	case errors.Is(err, domain.ErrEngineUnreachable), errors.Is(err, domain.ErrEngineRejected), errors.Is(err, domain.ErrEngineCommand):
		response.Err(w, http.StatusBadGateway, "ENGINE_UNAVAILABLE", "el motor de correo no pudo completar la operacion")
	case errors.Is(err, domain.ErrRedisUnavailable):
		response.Err(w, http.StatusServiceUnavailable, "REDIS_UNAVAILABLE", "el redis de los motores no responde")
	case errors.As(err, &verr):
		response.ErrValidation(w, verr.Msg)
	case errors.Is(err, domain.ErrNotConfigured):
		response.Err(w, http.StatusServiceUnavailable, "NOT_CONFIGURED", err.Error())
	default:
		response.Unexpected(w, err)
	}
}

// ── Umbrales ─────────────────────────────────────────────────────────────────

func (h *Handler) ListSpamScores(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	out, err := h.policy.ListSpamScores(r.Context(), tenantID)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, out)
}

func (h *Handler) GetSpamScore(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	out, err := h.policy.GetSpamScore(r.Context(), tenantID, chi.URLParam(r, "object"))
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, out)
}

func (h *Handler) PutSpamScore(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	var body struct {
		HighScore decimal.Decimal `json:"high_score"`
		LowScore  decimal.Decimal `json:"low_score"`
	}
	if !decode(w, r, &body) {
		return
	}
	out, err := h.policy.PutSpamScore(r.Context(), tenantID, chi.URLParam(r, "object"), body.HighScore, body.LowScore)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, out)
}

func (h *Handler) DeleteSpamScore(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	if err := h.policy.DeleteSpamScore(r.Context(), tenantID, chi.URLParam(r, "object")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Listas ────────────────────────────────────────────────────────────────────

func (h *Handler) ListAddressLists(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	out, err := h.policy.ListAddressLists(r.Context(), tenantID, r.URL.Query().Get("object"), domain.ListKind(r.URL.Query().Get("kind")))
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, out)
}

func (h *Handler) CreateAddressList(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	var body struct {
		Object  string          `json:"object"`
		Kind    domain.ListKind `json:"kind"`
		Pattern string          `json:"pattern"`
	}
	if !decode(w, r, &body) {
		return
	}
	out, err := h.policy.CreateAddressList(r.Context(), tenantID, body.Object, body.Kind, body.Pattern)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, out)
}

func (h *Handler) DeleteAddressList(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	if err := h.policy.DeleteAddressList(r.Context(), tenantID, id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Bloques adicionales ───────────────────────────────────────────────────────

func (h *Handler) ListSettingsMaps(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	out, err := h.policy.ListSettingsMaps(r.Context(), tenantID)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, out)
}

func (h *Handler) CreateSettingsMap(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	var body struct {
		Description string `json:"description"`
		Content     string `json:"content"`
		Active      *bool  `json:"active"`
	}
	if !decode(w, r, &body) {
		return
	}
	active := body.Active == nil || *body.Active
	out, err := h.policy.CreateSettingsMap(r.Context(), tenantID, body.Description, body.Content, active)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, out)
}

func (h *Handler) PatchSettingsMap(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		Description *string `json:"description"`
		Content     *string `json:"content"`
		Active      *bool   `json:"active"`
	}
	if !decode(w, r, &body) {
		return
	}
	out, err := h.policy.PatchSettingsMap(r.Context(), tenantID, id, app.SettingsMapPatch{Description: body.Description, Content: body.Content, Active: body.Active})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, out)
}

func (h *Handler) DeleteSettingsMap(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	if err := h.policy.DeleteSettingsMap(r.Context(), tenantID, id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Pies de pagina ────────────────────────────────────────────────────────────

func (h *Handler) ListFooters(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	out, err := h.policy.ListFooters(r.Context(), tenantID)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, out)
}

func (h *Handler) GetFooter(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	out, err := h.policy.GetFooter(r.Context(), tenantID, chi.URLParam(r, "domain"))
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, out)
}

func (h *Handler) PutFooter(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	var body struct {
		HTML               string   `json:"html"`
		Plain              string   `json:"plain"`
		MailboxExclude     []string `json:"mailbox_exclude"`
		AliasDomainExclude []string `json:"alias_domain_exclude"`
		SkipReplies        bool     `json:"skip_replies"`
	}
	if !decode(w, r, &body) {
		return
	}
	out, err := h.policy.PutFooter(r.Context(), tenantID, domain.DomainFooter{
		Domain: chi.URLParam(r, "domain"), HTML: body.HTML, Plain: body.Plain,
		MailboxExclude: body.MailboxExclude, AliasDomainExclude: body.AliasDomainExclude, SkipReplies: body.SkipReplies,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, out)
}

func (h *Handler) DeleteFooter(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	if err := h.policy.DeleteFooter(r.Context(), tenantID, chi.URLParam(r, "domain")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Hosts de reenvio ──────────────────────────────────────────────────────────

func (h *Handler) ListForwardingHosts(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	out, err := h.policy.ListForwardingHosts(r.Context(), tenantID)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, out)
}

func (h *Handler) CreateForwardingHost(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	var body struct {
		Host       string `json:"host"`
		Source     string `json:"source"`
		FilterSpam *bool  `json:"filter_spam"`
	}
	if !decode(w, r, &body) {
		return
	}
	filterSpam := body.FilterSpam == nil || *body.FilterSpam
	out, err := h.policy.CreateForwardingHost(r.Context(), tenantID, body.Host, body.Source, filterSpam)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, out)
}

func (h *Handler) DeleteForwardingHost(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	if err := h.policy.DeleteForwardingHost(r.Context(), tenantID, id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Limites de envio ──────────────────────────────────────────────────────────

func (h *Handler) ListRateLimits(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	out, err := h.policy.ListRateLimits(r.Context(), tenantID)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, out)
}

func (h *Handler) GetRateLimit(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	out, err := h.policy.GetRateLimit(r.Context(), tenantID, chi.URLParam(r, "object"))
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, out)
}

func (h *Handler) PutRateLimit(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	var body struct {
		Value string `json:"value"`
	}
	if !decode(w, r, &body) {
		return
	}
	out, err := h.policy.PutRateLimit(r.Context(), tenantID, chi.URLParam(r, "object"), body.Value)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, out)
}

func (h *Handler) DeleteRateLimit(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	if err := h.policy.DeleteRateLimit(r.Context(), tenantID, chi.URLParam(r, "object")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Etiquetas por buzon ───────────────────────────────────────────────────────

func (h *Handler) ListMailboxTags(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	out, err := h.policy.ListMailboxTags(r.Context(), tenantID)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, out)
}

func (h *Handler) GetMailboxTags(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	out, err := h.policy.GetMailboxTags(r.Context(), tenantID, chi.URLParam(r, "username"))
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, out)
}

func (h *Handler) PutMailboxTags(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	var body struct {
		SubjectTag   bool `json:"subject_tag"`
		SubfolderTag bool `json:"subfolder_tag"`
	}
	if !decode(w, r, &body) {
		return
	}
	out, err := h.policy.PutMailboxTags(r.Context(), tenantID, chi.URLParam(r, "username"), body.SubjectTag, body.SubfolderTag)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, out)
}

// ── Cuarentena ────────────────────────────────────────────────────────────────

func (h *Handler) ListQuarantine(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := domain.QuarantineFilter{Rcpt: q.Get("rcpt")}
	f.Page, _ = strconv.Atoi(q.Get("page"))
	f.PerPage, _ = strconv.Atoi(q.Get("per_page"))
	if s := q.Get("score_min"); s != "" {
		min, err := decimal.NewFromString(s)
		if err != nil {
			response.ErrBadRequest(w, "score_min invalido")
			return
		}
		f.ScoreMin = &min
	}
	items, total, err := h.quarantine.List(r.Context(), tenantID, f)
	if err != nil {
		writeError(w, err)
		return
	}
	if f.Page < 1 {
		f.Page = 1
	}
	if f.PerPage < 1 {
		f.PerPage = 50
	}
	response.JSONWithMeta(w, http.StatusOK, items, response.PageMeta(total, f.Page, f.PerPage))
}

func (h *Handler) GetQuarantine(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	out, err := h.quarantine.Get(r.Context(), tenantID, id)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, out)
}

func (h *Handler) GetQuarantineMessage(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	msg, err := h.quarantine.Message(r.Context(), tenantID, id)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "message/rfc822")
	w.Header().Set("Content-Disposition", `attachment; filename="`+id.String()+`.eml"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(msg)
}

func (h *Handler) DeleteQuarantine(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	if err := h.quarantine.Delete(r.Context(), tenantID, id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) ReleaseQuarantine(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	if err := h.quarantine.Release(r.Context(), tenantID, id, middleware.GetUserID(r.Context())); err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": "released"})
}

// ReleaseQuarantineAsHam libera el mensaje y lo usa para entrenar el clasificador como legitimo: exige el
// permiso de liberar y el de entrenar.
func (h *Handler) ReleaseQuarantineAsHam(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	if err := h.quarantine.ReleaseAndLearnHam(r.Context(), tenantID, id, middleware.GetUserID(r.Context())); err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": "released"})
}

func (h *Handler) LearnSpam(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	if err := h.quarantine.LearnSpam(r.Context(), tenantID, id); err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": "learned"})
}

// ── Ajustes de cuarentena ─────────────────────────────────────────────────────

func (h *Handler) GetQuarantineSettings(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	out, err := h.policy.GetQuarantineSettings(r.Context(), tenantID)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, out)
}

func (h *Handler) PutQuarantineSettings(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	var body struct {
		MaxSizeBytes   int64                   `json:"max_size_bytes"`
		MaxAgeDays     int                     `json:"max_age_days"`
		RetentionSize  int                     `json:"retention_size"`
		ExcludeDomains []string                `json:"exclude_domains"`
		Notify         domain.QuarantineNotify `json:"notify"`
	}
	if !decode(w, r, &body) {
		return
	}
	out, err := h.policy.PutQuarantineSettings(r.Context(), tenantID, domain.QuarantineSettings{
		MaxSizeBytes: body.MaxSizeBytes, MaxAgeDays: body.MaxAgeDays, RetentionSize: body.RetentionSize,
		ExcludeDomains: body.ExcludeDomains, Notify: body.Notify,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, out)
}

// ── DKIM (interno) ────────────────────────────────────────────────────────────

type dkimKeyBody struct {
	Selector      string `json:"selector"`
	PrivateKeyPEM string `json:"private_key_pem"`
}

// PutDKIM recibe de domain-service el juego completo de claves del dominio en keys, en orden (la
// ultima firma; los demas selectores se retiran). selector y private_key_pem son la forma
// anterior, una clave por llamada. Un dominio que la celda no sirve es 409 DKIM_DOMAIN_NOT_ACTIVE.
func (h *Handler) PutDKIM(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	var body struct {
		dkimKeyBody
		Keys []dkimKeyBody `json:"keys"`
	}
	if !decode(w, r, &body) {
		return
	}
	name := chi.URLParam(r, "domain")
	var err error
	switch {
	case body.Keys != nil && body.dkimKeyBody != (dkimKeyBody{}):
		err = &domain.ValidationError{Msg: "keys no se combina con selector ni private_key_pem"}
	case body.Keys != nil:
		keys := make([]domain.DKIMKey, 0, len(body.Keys))
		for _, k := range body.Keys {
			keys = append(keys, domain.DKIMKey{Domain: name, Selector: k.Selector, PrivateKeyPEM: k.PrivateKeyPEM})
		}
		err = h.dkim.PutKeys(r.Context(), tenantID, name, keys)
	default:
		err = h.dkim.PutKey(r.Context(), tenantID, domain.DKIMKey{Domain: name, Selector: body.Selector, PrivateKeyPEM: body.PrivateKeyPEM})
	}
	if err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) DeleteDKIMDomain(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	if err := h.dkim.DeleteDomain(r.Context(), tenantID, chi.URLParam(r, "domain")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) DeleteDKIMSelector(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	if err := h.dkim.DeleteSelector(r.Context(), tenantID, chi.URLParam(r, "domain"), chi.URLParam(r, "selector")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
