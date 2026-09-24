package http

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/transactional/internal/adapters/sns"
	"github.com/alonsosss/corforce-email/services/transactional/internal/app"
	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	// maxCreateBody deja margen sobre MaxBodyBytes para el resto del JSON.
	maxCreateBody = int64(domain.MaxBodyBytes) + 512<<10
	// maxSNSBody acota las notificaciones de SNS.
	maxSNSBody = 1 << 20
	// maxUnsubscribeBody acota el POST de baja (One-Click manda una linea).
	maxUnsubscribeBody = 4 << 10
	// maxBatchBody acota un lote de campana: hasta 500 destinatarios con sus variables.
	maxBatchBody = 8 << 20

	permModule     = "transactional"
	defaultPerPage = 25
	maxPerPage     = 100
)

// PermissionGuard es la tercera capa de acceso (pkg/authz.Checker la cumple).
type PermissionGuard interface {
	RequirePermission(module, resource, action string) func(http.Handler) http.Handler
}

type Deps struct {
	UC       *app.UseCase
	TenantDB *db.TenantDB
	Perms    PermissionGuard
	SNS      *sns.Verifier
	// TopicARN (SES_EVENTS_TOPIC_ARN): el unico topic cuyas notificaciones se aceptan. Sin el, las
	// dos rutas de eventos rechazan todo.
	TopicARN string
	Logger   *zap.Logger
}

type Handler struct {
	uc       *app.UseCase
	tenantDB *db.TenantDB
	perms    PermissionGuard
	sns      *sns.Verifier
	topicARN string
	logger   *zap.Logger
}

func NewHandler(d Deps) *Handler {
	return &Handler{uc: d.UC, tenantDB: d.TenantDB, perms: d.Perms, sns: d.SNS, topicARN: d.TopicARN, logger: d.Logger}
}

// Routes monta tres superficies: el API con sesion (por el gateway), las rutas publicas
// (webhook de SNS, enlace de baja y correo en el navegador, sin sesion) y los endpoints
// internos (envio de la plataforma, lotes de campana y pruebas de plantilla).
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	apiLimiter := middleware.NewRateLimiter(120, time.Minute)
	publicLimiter := middleware.NewRateLimiter(60, time.Minute)

	r.Route("/api/v1/transactional", func(r chi.Router) {
		r.Get("/health", h.Health)
		r.Group(func(r chi.Router) {
			r.Use(middleware.InjectFromGateway)
			r.Use(db.TenantPoolMiddleware(h.tenantDB))
			r.Use(apiLimiter.LimitPerUser)
			r.With(h.perms.RequirePermission(permModule, "messages", "create")).Post("/messages", h.CreateMessages)
			r.With(h.perms.RequirePermission(permModule, "messages", "read")).Get("/messages", h.ListMessages)
			r.With(h.perms.RequirePermission(permModule, "messages", "read")).Get("/messages/{id}", h.GetMessage)
			r.With(h.perms.RequirePermission(permModule, "messages", "read")).Get("/messages/{id}/events", h.ListEvents)
			r.With(h.perms.RequirePermission(permModule, "stats", "read")).Get("/stats", h.Stats)
			r.With(h.perms.RequirePermission(permModule, "sending_domains", "read")).Get("/sending-domains", h.ListSendingDomains)
		})
	})

	r.Route("/api/v1/public/transactional", func(r chi.Router) {
		// Sin empresa en la ruta: una sola suscripcion SNS para toda la plataforma. Con
		// empresa: una cuenta de SES propia de esa empresa con su propio topic.
		r.Post("/ses-events", h.SESEvents)
		r.Post("/ses-events/{tenantID}", h.SESEvents)
		r.With(publicLimiter.Limit).Get("/unsubscribe", h.UnsubscribePage)
		r.With(publicLimiter.Limit).Post("/unsubscribe", h.Unsubscribe)
		r.With(publicLimiter.Limit).Get("/view", h.ViewInBrowser)
	})

	r.Route("/internal", func(r chi.Router) {
		r.Use(middleware.InjectFromGateway)
		r.Use(db.TenantHeaderPoolMiddleware(h.tenantDB))
		r.Post("/send-email", h.InternalSendEmail)
		r.Post("/transactional/messages", h.InternalCreateMessages)
		r.Post("/transactional/batch", h.MarketingBatch)
		r.Post("/transactional/test-send", h.TemplateTestSend)
		r.Post("/transactional/raw-messages", h.InternalRawMessage)
	})
	return r
}

func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	response.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ── API con sesion ───────────────────────────────────────────────────────────

type recipientDTO struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
}

type createRequest struct {
	IdempotencyKey  string            `json:"idempotency_key,omitempty"`
	From            recipientDTO      `json:"from"`
	ReplyTo         string            `json:"reply_to,omitempty"`
	To              []recipientDTO    `json:"to"`
	Cc              []recipientDTO    `json:"cc,omitempty"`
	Bcc             []recipientDTO    `json:"bcc,omitempty"`
	Subject         string            `json:"subject,omitempty"`
	HTML            string            `json:"html,omitempty"`
	Text            string            `json:"text,omitempty"`
	TemplateID      *uuid.UUID        `json:"template_id,omitempty"`
	TemplateVersion *int              `json:"template_version,omitempty"`
	Variables       map[string]any    `json:"variables,omitempty"`
	Headers         map[string]string `json:"headers,omitempty"`
	Tags            map[string]string `json:"tags,omitempty"`
	ScheduledAt     *time.Time        `json:"scheduled_at,omitempty"`
	Unsubscribable  bool              `json:"unsubscribable,omitempty"`
	// Attachments se declara solo para rechazarlo con un codigo claro (422) en vez de
	// un "campo desconocido": esta fase no admite adjuntos.
	Attachments json.RawMessage `json:"attachments,omitempty"`
}

func (h *Handler) CreateMessages(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFromContext(w, r)
	if !ok {
		return
	}
	var req createRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxCreateBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	cmd := req.command(tenantID, idempotencyKey(r, req))
	if uid, err := uuid.Parse(middleware.GetUserID(r.Context())); err == nil {
		cmd.CreatedBy = &uid
	}
	if keyID, err := uuid.Parse(middleware.GetAPIKeyID(r.Context())); err == nil {
		cmd.APIKeyID = &keyID
	}
	h.createMessages(w, r, cmd)
}

// idempotencyKey: la cabecera manda sobre el campo del cuerpo.
func idempotencyKey(r *http.Request, req createRequest) string {
	if key := strings.TrimSpace(r.Header.Get("Idempotency-Key")); key != "" {
		return key
	}
	return strings.TrimSpace(req.IdempotencyKey)
}

func (req createRequest) command(tenantID uuid.UUID, key string) app.CreateMessagesCommand {
	return app.CreateMessagesCommand{
		TenantID:        tenantID,
		IdempotencyKey:  key,
		From:            domain.Recipient(req.From),
		ReplyTo:         req.ReplyTo,
		To:              toRecipients(req.To),
		Cc:              toRecipients(req.Cc),
		Bcc:             toRecipients(req.Bcc),
		Subject:         req.Subject,
		HTML:            req.HTML,
		Text:            req.Text,
		TemplateID:      req.TemplateID,
		TemplateVersion: req.TemplateVersion,
		Variables:       req.Variables,
		Headers:         req.Headers,
		Tags:            req.Tags,
		ScheduledAt:     req.ScheduledAt,
		Unsubscribable:  req.Unsubscribable,
		HasAttachments:  len(req.Attachments) > 0 && string(req.Attachments) != "null" && string(req.Attachments) != "[]",
	}
}

// createMessages responde 202 al crear y 200 ante una repeticion de la clave o si todos
// los destinatarios estaban suprimidos.
func (h *Handler) createMessages(w http.ResponseWriter, r *http.Request, cmd app.CreateMessagesCommand) {
	result, err := h.uc.CreateMessages(r.Context(), cmd)
	if err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusAccepted
	if result.Replayed || allSuppressed(result) {
		status = http.StatusOK
	}
	response.JSON(w, status, result)
}

// internalCreateRequest es el cuerpo de POST /messages mas el proposito, que solo admite
// esta ruta interna.
type internalCreateRequest struct {
	createRequest
	Purpose string `json:"purpose,omitempty"`
}

// InternalCreateMessages es el envio de una empresa para otro servicio de la plataforma
// (automations con el doble opt-in, mail-security con el aviso de cuarentena): mismo
// cuerpo y mismas reglas que POST /messages, la empresa en X-Tenant-ID, Idempotency-Key
// obligatoria (por cabecera o en el cuerpo) y un proposito opcional (domain.Purposes).
func (h *Handler) InternalCreateMessages(w http.ResponseWriter, r *http.Request) {
	tenantID, err := uuid.Parse(r.Header.Get("X-Tenant-ID"))
	if err != nil {
		response.ErrUnauthorized(w, "invalid tenant")
		return
	}
	var req internalCreateRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxCreateBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	key := idempotencyKey(r, req.createRequest)
	if key == "" {
		response.ErrValidation(w, "Idempotency-Key is required on this route")
		return
	}
	cmd := req.command(tenantID, key)
	cmd.Purpose = req.Purpose
	h.createMessages(w, r, cmd)
}

func allSuppressed(res *app.CreateResult) bool {
	for _, m := range res.Messages {
		if m.Status != domain.StatusSuppressed {
			return false
		}
	}
	return len(res.Messages) > 0
}

func (h *Handler) ListMessages(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFromContext(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	filter := domain.MessageFilter{
		Status: strings.TrimSpace(q.Get("status")),
		Class:  strings.TrimSpace(q.Get("class")),
		To:     domain.NormalizeEmail(q.Get("to")),
		From:   domain.NormalizeEmail(q.Get("from")),
	}
	var err error
	if filter.DateFrom, err = optionalTime(q.Get("date_from")); err != nil {
		response.ErrValidation(w, "date_from must be RFC 3339")
		return
	}
	if filter.DateTo, err = optionalTime(q.Get("date_to")); err != nil {
		response.ErrValidation(w, "date_to must be RFC 3339")
		return
	}
	page, perPage := pagination(q.Get("page"), q.Get("per_page"))
	list, total, err := h.uc.ListMessages(r.Context(), tenantID, filter, page, perPage)
	if err != nil {
		writeError(w, err)
		return
	}
	// El listado no lleva cuerpos ni variables: pesan y pueden llevar datos personales;
	// quien necesita el contenido abre el mensaje.
	for i := range list {
		list[i].HTML, list[i].Text, list[i].Variables = nil, nil, nil
	}
	response.JSONWithMeta(w, http.StatusOK, list, response.PageMeta(total, page, perPage))
}

func (h *Handler) GetMessage(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFromContext(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.ErrValidation(w, "id must be a valid UUID")
		return
	}
	msg, err := h.uc.GetMessage(r.Context(), tenantID, id)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, msg)
}

func (h *Handler) ListEvents(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFromContext(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.ErrValidation(w, "id must be a valid UUID")
		return
	}
	events, err := h.uc.ListEvents(r.Context(), tenantID, id)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, events)
}

func (h *Handler) Stats(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFromContext(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	now := time.Now().UTC()
	from, err := optionalTime(q.Get("from"))
	if err != nil {
		response.ErrValidation(w, "from must be RFC 3339")
		return
	}
	to, err := optionalTime(q.Get("to"))
	if err != nil {
		response.ErrValidation(w, "to must be RFC 3339")
		return
	}
	if to == nil {
		to = &now
	}
	if from == nil {
		start := to.Add(-30 * 24 * time.Hour)
		from = &start
	}
	stats, err := h.uc.Stats(r.Context(), tenantID, *from, *to)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]any{"from": from, "to": to, "by_status": stats})
}

func (h *Handler) ListSendingDomains(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFromContext(w, r)
	if !ok {
		return
	}
	list, err := h.uc.ListSendingDomains(r.Context(), tenantID)
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]sendingDomainResponse, len(list))
	for i := range list {
		out[i] = sendingDomainResponse{SendingDomain: list[i], CanSend: list[i].CanSend()}
	}
	response.JSON(w, http.StatusOK, out)
}

// sendingDomainResponse anade can_send: si hoy se puede enviar desde el dominio, con la misma
// regla que aplica el envio (domain.SendingDomain.CanSend). La interfaz ofrece como remitente
// solo esos dominios sin repetir la regla.
type sendingDomainResponse struct {
	domain.SendingDomain
	CanSend bool `json:"can_send"`
}

// ── Interno (plataforma) ─────────────────────────────────────────────────────

type internalSendRequest struct {
	To       string `json:"to"`
	Subject  string `json:"subject"`
	HTMLBody string `json:"html_body"`
	TextBody string `json:"text_body,omitempty"`
}

// InternalSendEmail es el contrato que usa identity (mailerclient): 200 = aceptado.
func (h *Handler) InternalSendEmail(w http.ResponseWriter, r *http.Request) {
	tenantID, err := uuid.Parse(r.Header.Get("X-Tenant-ID"))
	if err != nil {
		response.ErrUnauthorized(w, "invalid tenant")
		return
	}
	var req internalSendRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxCreateBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	result, err := h.uc.InternalSend(r.Context(), app.InternalSendCommand{
		TenantID: tenantID, To: req.To, Subject: req.Subject, HTMLBody: req.HTMLBody, TextBody: req.TextBody,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, result)
}

type batchRecipientDTO struct {
	Email     string         `json:"email"`
	Name      string         `json:"name,omitempty"`
	ContactID *uuid.UUID     `json:"contact_id"`
	Variables map[string]any `json:"variables,omitempty"`
}

type batchRequest struct {
	Class           string              `json:"class"`
	CampaignID      *uuid.UUID          `json:"campaign_id"`
	IdempotencyKey  string              `json:"idempotency_key"`
	From            recipientDTO        `json:"from"`
	ReplyTo         string              `json:"reply_to,omitempty"`
	TemplateID      *uuid.UUID          `json:"template_id"`
	TemplateVersion *int                `json:"template_version"`
	Subject         string              `json:"subject,omitempty"`
	Recipients      []batchRecipientDTO `json:"recipients"`
	Tags            map[string]string   `json:"tags,omitempty"`
	UTM             *batchUTMDTO        `json:"utm,omitempty"`
}

type batchUTMDTO struct {
	Enabled  *bool  `json:"enabled,omitempty"`
	Source   string `json:"source,omitempty"`
	Campaign string `json:"campaign,omitempty"`
	Content  string `json:"content,omitempty"`
}

// MarketingBatch es el contrato que usa campaigns: 202 al crear el lote (tambien con todos
// los destinatarios suprimidos, accepted 0), 200 con el mismo cuerpo ante una repeticion
// de la clave. Las denegaciones de reputation y las caidas se traducen en writeError.
func (h *Handler) MarketingBatch(w http.ResponseWriter, r *http.Request) {
	tenantID, err := uuid.Parse(r.Header.Get("X-Tenant-ID"))
	if err != nil {
		response.ErrUnauthorized(w, "invalid tenant")
		return
	}
	var req batchRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxBatchBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	cmd := app.MarketingBatchCommand{
		TenantID:       tenantID,
		Class:          strings.TrimSpace(req.Class),
		IdempotencyKey: req.IdempotencyKey,
		From:           domain.Recipient(req.From),
		ReplyTo:        req.ReplyTo,
		Subject:        req.Subject,
		Tags:           req.Tags,
		Recipients:     make([]app.MarketingRecipient, len(req.Recipients)),
	}
	if req.CampaignID != nil {
		cmd.CampaignID = *req.CampaignID
	}
	if req.TemplateID != nil {
		cmd.TemplateID = *req.TemplateID
	}
	if req.TemplateVersion != nil {
		cmd.TemplateVersion = *req.TemplateVersion
	}
	if req.UTM != nil {
		cmd.UTM = &app.UTMInput{Enabled: req.UTM.Enabled, Source: req.UTM.Source, Campaign: req.UTM.Campaign, Content: req.UTM.Content}
	}
	for i, rcpt := range req.Recipients {
		cmd.Recipients[i] = app.MarketingRecipient{Email: rcpt.Email, Name: rcpt.Name, Variables: rcpt.Variables}
		if rcpt.ContactID != nil {
			cmd.Recipients[i].ContactID = *rcpt.ContactID
		}
	}
	result, err := h.uc.CreateMarketingBatch(r.Context(), cmd)
	if err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusAccepted
	if result.Replayed {
		status = http.StatusOK
	}
	response.JSON(w, status, result)
}

// ── Webhook de SNS (eventos de SES) ──────────────────────────────────────────

// SESEvents recibe los eventos de SES que publica SNS. La empresa sale de la etiqueta
// tenant_id que el propio emisor pone en cada envio: SES la devuelve dentro del mensaje
// firmado por SNS, y con la firma y el topic comprobados nadie de fuera puede fabricarla.
// Por eso las dos rutas exigen SES_EVENTS_TOPIC_ARN: una firma valida de SNS solo prueba que el
// mensaje lo firmo AWS, y cualquier cuenta de AWS puede crear un topic y suscribir esta URL. Sin
// topic fijado, quien conozca el identificador de una empresa (el enlace de baja de cualquier
// correo lo lleva) se atribuiria bajas y quejas de las direcciones que elija.
func (h *Handler) SESEvents(w http.ResponseWriter, r *http.Request) {
	if h.topicARN == "" {
		h.logger.Warn("transactional: evento de SES sin SES_EVENTS_TOPIC_ARN; se rechaza")
		response.ErrForbidden(w, "events topic not configured")
		return
	}
	var routeTenant uuid.UUID
	if raw := chi.URLParam(r, "tenantID"); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			response.ErrBadRequest(w, "invalid tenant")
			return
		}
		routeTenant = parsed
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxSNSBody)
	var env sns.Envelope
	if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
		response.ErrBadRequest(w, "invalid SNS envelope")
		return
	}
	// El topic va antes de la firma: un peticionario anonimo no obliga al servicio a descargar
	// certificados. Una vez verificada la firma, el TopicArn ya no puede ser otro (va firmado).
	if env.TopicArn != h.topicARN {
		h.logger.Warn("transactional: notificacion de un topic no esperado", zap.String("topic", env.TopicArn))
		h.uc.NoteSESEventRejected(domain.EventRejectTopic)
		response.ErrForbidden(w, "unexpected topic")
		return
	}
	if err := h.sns.Verify(r.Context(), &env); err != nil {
		h.logger.Warn("transactional: notificacion SNS rechazada", zap.String("route_tenant", routeTenant.String()),
			zap.String("sns_message_id", env.MessageID), zap.String("topic", env.TopicArn), zap.Error(err))
		h.uc.NoteSESEventRejected(domain.EventRejectSignature)
		response.ErrForbidden(w, "invalid SNS signature")
		return
	}

	switch env.Type {
	case sns.TypeSubscriptionConfirmation:
		if err := h.sns.ConfirmSubscription(r.Context(), env.SubscribeURL); err != nil {
			h.logger.Warn("transactional: no se confirmo la suscripcion SNS", zap.Error(err))
			response.ErrForbidden(w, "subscription not confirmed")
			return
		}
		h.logger.Info("transactional: suscripcion SNS confirmada", zap.String("topic", env.TopicArn))
		response.JSON(w, http.StatusOK, map[string]string{"status": "confirmed"})
		return
	case sns.TypeUnsubscribeConfirmation:
		h.logger.Warn("transactional: SNS notifico la baja de la suscripcion", zap.String("topic", env.TopicArn))
		response.JSON(w, http.StatusOK, map[string]string{"status": "acknowledged"})
		return
	case sns.TypeNotification:
	default:
		response.ErrBadRequest(w, "unknown SNS message type")
		return
	}

	ev, err := sns.ParseSESEvent([]byte(env.Message), env.MessageID)
	// Firmado y de nuestro topic, pero sin las etiquetas del servicio: lo envio otro emisor de la
	// cuenta (la consola de SES, un conjunto por defecto del dominio). No hay mensaje al que
	// atribuirlo y un 4xx solo haria que SNS lo reintentara.
	if errors.Is(err, sns.ErrNotOurs) {
		h.logger.Warn("transactional: evento de SES sin etiquetas del servicio; se ignora",
			zap.String("sns_message_id", env.MessageID), zap.Error(err))
		h.uc.NoteSESEventRejected(domain.EventRejectUntagged)
		response.JSON(w, http.StatusOK, map[string]string{"status": "ignored"})
		return
	}
	if err != nil {
		h.logger.Warn("transactional: evento de SES ilegible", zap.String("sns_message_id", env.MessageID), zap.Error(err))
		h.uc.NoteSESEventRejected(domain.EventRejectUnreadable)
		response.ErrBadRequest(w, "invalid SES event")
		return
	}
	if routeTenant != uuid.Nil && ev.TenantID != routeTenant {
		h.logger.Warn("transactional: evento de SES de otra empresa", zap.String("route_tenant", routeTenant.String()),
			zap.String("event_tenant", ev.TenantID.String()), zap.String("sns_message_id", env.MessageID))
		h.uc.NoteSESEventRejected(domain.EventRejectTenantMismatch)
		response.ErrForbidden(w, "tenant mismatch")
		return
	}
	h.uc.NoteSESEvent(ev.Type)
	tenant := ev.TenantID
	pool, err := h.tenantDB.ResolveForTenant(r.Context(), tenant.String())
	if err != nil {
		if db.IsUnknownTenant(err) {
			response.ErrNotFound(w, "tenant not found")
			return
		}
		h.logger.Error("transactional: base de la empresa no disponible para el evento de SES", zap.Error(err))
		response.Err(w, http.StatusServiceUnavailable, "DATABASE_UNAVAILABLE", "database unavailable")
		return
	}
	ctx := db.WithTenant(r.Context(), pool, tenant.String())
	if err := h.uc.IngestSESEvent(ctx, tenant, ev); err != nil {
		switch {
		case app.IsIgnorableIngestError(err):
			h.logger.Warn("transactional: evento de SES para un mensaje desconocido; se ignora",
				zap.String("message_id", ev.MessageID.String()), zap.String("type", ev.Type))
			response.JSON(w, http.StatusOK, map[string]string{"status": "ignored"})
		case errors.Is(err, domain.ErrTenantMismatch):
			response.ErrForbidden(w, "tenant mismatch")
		case errors.Is(err, domain.ErrSuppressionUnavailable):
			response.Err(w, http.StatusServiceUnavailable, "SUPPRESSION_UNAVAILABLE", "suppression unavailable")
		case domain.IsValidation(err):
			response.ErrBadRequest(w, err.Error())
		default:
			// SNS reintenta ante 5xx: es lo que se quiere cuando la base no responde.
			h.logger.Error("transactional: no se pudo registrar el evento de SES", zap.Error(err))
			response.Err(w, http.StatusServiceUnavailable, "DATABASE_UNAVAILABLE", "event not stored")
		}
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ── Baja RFC 8058 ────────────────────────────────────────────────────────────

func (h *Handler) unsubscribeClaims(r *http.Request) (domain.UnsubscribeClaims, string, error) {
	q := r.URL.Query()
	get := func(name string) string {
		if v := q.Get(name); v != "" {
			return v
		}
		return r.PostFormValue(name)
	}
	tenantID, err := uuid.Parse(get("t"))
	if err != nil {
		return domain.UnsubscribeClaims{}, "", domain.ErrInvalidSignature
	}
	messageID, err := uuid.Parse(get("m"))
	if err != nil {
		return domain.UnsubscribeClaims{}, "", domain.ErrInvalidSignature
	}
	return domain.UnsubscribeClaims{TenantID: tenantID, MessageID: messageID, Email: get("e")}, get("sig"), nil
}

// UnsubscribePage muestra la confirmacion (GET): un boton que hace POST a la misma URL.
func (h *Handler) UnsubscribePage(w http.ResponseWriter, r *http.Request) {
	claims, sig, err := h.unsubscribeClaims(r)
	if err == nil {
		err = h.uc.VerifyUnsubscribeLink(claims, sig)
	}
	if err != nil {
		writePage(w, http.StatusForbidden, pageInvalidLink)
		return
	}
	writePage(w, http.StatusOK, pageConfirm(r.URL.RequestURI(), claims.Email))
}

// Unsubscribe registra la baja (POST): desde la pagina o directamente desde el cliente
// de correo con List-Unsubscribe=One-Click.
func (h *Handler) Unsubscribe(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUnsubscribeBody)
	claims, sig, err := h.unsubscribeClaims(r)
	if err != nil {
		writePage(w, http.StatusForbidden, pageInvalidLink)
		return
	}
	if err := h.uc.VerifyUnsubscribeLink(claims, sig); err != nil {
		writePage(w, http.StatusForbidden, pageInvalidLink)
		return
	}
	pool, err := h.tenantDB.ResolveForTenant(r.Context(), claims.TenantID.String())
	if err != nil {
		if db.IsUnknownTenant(err) {
			writePage(w, http.StatusForbidden, pageInvalidLink)
			return
		}
		h.logger.Error("transactional: base de la empresa no disponible para la baja", zap.Error(err))
		writePage(w, http.StatusServiceUnavailable, pageUnavailable)
		return
	}
	ctx := db.WithTenant(r.Context(), pool, claims.TenantID.String())
	if err := h.uc.Unsubscribe(ctx, claims, sig); err != nil {
		if errors.Is(err, domain.ErrInvalidSignature) {
			writePage(w, http.StatusForbidden, pageInvalidLink)
			return
		}
		h.logger.Error("transactional: la baja no se pudo registrar", zap.Error(err))
		writePage(w, http.StatusServiceUnavailable, pageUnavailable)
		return
	}
	writePage(w, http.StatusOK, pageDone(claims.Email))
}

// ── Utilidades ───────────────────────────────────────────────────────────────

func tenantFromContext(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	tenantID, err := uuid.Parse(middleware.GetTenantID(r.Context()))
	if err != nil {
		response.ErrUnauthorized(w, "invalid tenant")
		return uuid.Nil, false
	}
	return tenantID, true
}

func toRecipients(in []recipientDTO) []domain.Recipient {
	if in == nil {
		return nil
	}
	out := make([]domain.Recipient, len(in))
	for i, r := range in {
		out[i] = domain.Recipient(r)
	}
	return out
}

func optionalTime(s string) (*time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func pagination(pageStr, perPageStr string) (int, int) {
	page, _ := strconv.Atoi(pageStr)
	if page < 1 {
		page = 1
	}
	perPage, _ := strconv.Atoi(perPageStr)
	if perPage < 1 {
		perPage = defaultPerPage
	}
	if perPage > maxPerPage {
		perPage = maxPerPage
	}
	return page, perPage
}

// writeDenied traduce una denegacion de reputation. El mensaje es el motivo tal cual lo da
// reputation, para que el llamador pueda decidir:
//   - rate_limited con espera: 429 RATE_LIMITED y Retry-After en segundos;
//   - rate_limited sin espera (lo pedido supera el propio limite de la ventana):
//     403 SENDING_RESTRICTED, porque reintentar igual nunca pasara;
//   - suspended y reputation_restricted: 403 SENDING_RESTRICTED;
//   - cualquier otro motivo es del plan (billing): 403 PLAN_LIMIT_REACHED.
func writeDenied(w http.ResponseWriter, d *domain.SendingDeniedError) {
	switch {
	case d.RateLimited():
		seconds := int(math.Ceil(d.RetryAfter.Seconds()))
		if seconds < 1 {
			seconds = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		response.Err(w, http.StatusTooManyRequests, "RATE_LIMITED", d.Reason)
	case d.ExceedsRateWindow():
		response.Err(w, http.StatusForbidden, "SENDING_RESTRICTED",
			d.Reason+": lo pedido supera el límite de tasa de la ventana; esperar no basta, hay que partir el envío")
	case d.Restricted():
		response.Err(w, http.StatusForbidden, "SENDING_RESTRICTED", d.Reason)
	default:
		reason := d.Reason
		if reason == "" {
			reason = domain.DenyPlanDenied
		}
		response.Err(w, http.StatusForbidden, "PLAN_LIMIT_REACHED", reason)
	}
}

func writeError(w http.ResponseWriter, err error) {
	var denied *domain.SendingDeniedError
	switch {
	case errors.As(err, &denied):
		writeDenied(w, denied)
	case errors.Is(err, domain.ErrTestSendLimit):
		response.Err(w, http.StatusTooManyRequests, "TEST_SEND_LIMIT_REACHED", "se alcanzó el tope de envíos de prueba de la última hora")
	case errors.Is(err, domain.ErrReputationUnavailable):
		response.Err(w, http.StatusServiceUnavailable, "REPUTATION_UNAVAILABLE", "reputation no respondió; no se encoló nada")
	case errors.Is(err, domain.ErrTemplateNotMarketing):
		response.Err(w, http.StatusUnprocessableEntity, "TEMPLATE_NOT_MARKETING", "la plantilla no es de tipo marketing")
	case errors.Is(err, domain.ErrTemplateMissingUnsubscribe):
		response.Err(w, http.StatusUnprocessableEntity, "TEMPLATE_MISSING_UNSUBSCRIBE", "la plantilla de marketing no lleva el enlace de baja obligatorio")
	case errors.Is(err, domain.ErrTemplateNotTransactional):
		response.Err(w, http.StatusUnprocessableEntity, "TEMPLATE_NOT_TRANSACTIONAL", "la plantilla es de marketing: una campaña sale por la vía de marketing")
	case errors.Is(err, domain.ErrIdempotencyKeyReused):
		response.Err(w, http.StatusConflict, "IDEMPOTENCY_KEY_REUSED", "la clave de idempotencia ya identifica una petición de otra clase")
	case errors.Is(err, domain.ErrNotFound):
		response.ErrNotFound(w, "recurso no encontrado")
	case errors.Is(err, domain.ErrAttachmentsNotSupported):
		response.Err(w, http.StatusUnprocessableEntity, "ATTACHMENTS_NOT_SUPPORTED", "los adjuntos no se admiten en esta fase")
	case errors.Is(err, domain.ErrSendingDomainNotVerified):
		response.Err(w, http.StatusUnprocessableEntity, "SENDING_DOMAIN_NOT_VERIFIED", "el dominio del remitente no está verificado para envío")
	case errors.Is(err, domain.ErrTemplateNotFound):
		response.Err(w, http.StatusUnprocessableEntity, "TEMPLATE_NOT_FOUND", "la plantilla o su versión no existe")
	case errors.Is(err, domain.ErrSuppressionUnavailable):
		response.Err(w, http.StatusServiceUnavailable, "SUPPRESSION_UNAVAILABLE", "la lista de supresión no está disponible; no se encoló nada")
	case errors.Is(err, domain.ErrTemplatesUnavailable):
		response.Err(w, http.StatusServiceUnavailable, "TEMPLATES_UNAVAILABLE", "el servicio de plantillas no está disponible")
	case errors.Is(err, domain.ErrTenantMismatch), errors.Is(err, domain.ErrInvalidSignature):
		response.ErrForbidden(w, "operación no permitida")
	case domain.IsValidation(err):
		response.ErrValidation(w, err.Error())
	default:
		response.Unexpected(w, err)
	}
}
