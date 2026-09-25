// Package http expone el API del webmail bajo /api/v1/webmail.
//
// Es un prefijo autenticado por el propio servicio en el gateway (sin JWT de la
// plataforma): cada peticion se autentica con la cookie de sesion del webmail y toda
// escritura exige un Origin permitido ademas de SameSite=Strict.
package http

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/services/webmail/internal/app"
	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

// BasePath es el prefijo del API; tambien es el Path de la cookie de sesion.
const BasePath = "/api/v1/webmail"

// apiCSP es la politica de toda respuesta del API: JSON o bytes de un adjunto, nunca un
// documento. Si alguien navega a una de estas URLs, nada se ejecuta ni se incrusta.
const apiCSP = "default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'; sandbox"

type Config struct {
	CookieSecure     bool
	SessionIdle      time.Duration
	SessionMax       time.Duration
	AllowedOrigins   []string
	MaxMessageBytes  int64
	OperationTimeout time.Duration
	// TransferTimeout es el plazo de descargas de adjuntos y envios con adjuntos grandes.
	TransferTimeout time.Duration
	// EventsHeartbeat, EventsSessionCheck y EventsMaxLifetime rigen el flujo de avisos; 0 usa los valores de
	// siempre (25 s, 10 s y 100 s).
	EventsHeartbeat    time.Duration
	EventsSessionCheck time.Duration
	EventsMaxLifetime  time.Duration
	// MaxLargeFileBytes es lo que el webmail deja pasar hacia mail-files en una subida de fichero
	// grande; el tope exacto lo aplica mail-files.
	MaxLargeFileBytes int64
	// ComposeConcurrency es cuantos envios o borradores se componen a la vez en el proceso; 0
	// usa defaultComposeConcurrency.
	ComposeConcurrency int
	// MFAChallengeTTL es la vida de la cookie del segundo paso: la misma que la del desafio.
	MFAChallengeTTL time.Duration
	// IPRateLimiter acota por IP lo que no tiene sesion (inicio y cierre de sesion, segundo paso,
	// pagina publica de citas y cookies que no abren sesion); MailboxRateLimiter acota por buzon
	// todo lo que se sirve con sesion, de modo que una oficina tras una sola IP no comparte cupo.
	IPRateLimiter      RateLimiter
	MailboxRateLimiter RateLimiter
	// ImageProxyRateLimiter acota por buzon las descargas del proxy de imagenes remotas (el buzon que
	// firma el enlace, que no lleva sesion), con su propio cupo: un boletin con muchas imagenes no
	// agota el del resto del webmail. ImageProxyConcurrency es cuantas descargas corren a la vez en el
	// proceso (0 usa defaultImageProxyConcurrency) e ImageProxyMetrics, opcional, las cuenta.
	ImageProxyRateLimiter RateLimiter
	ImageProxyConcurrency int
	ImageProxyMetrics     ImageProxyMetrics
}

// RateLimiter decide si una peticion cabe en su cupo y cuanto falta para que se reabra
// (pkg/middleware.RateLimiter lo cumple).
type RateLimiter interface {
	AllowIP(ctx context.Context, ip string) (bool, time.Duration)
	AllowKey(ctx context.Context, key string) (bool, time.Duration)
}

// defaultComposeConcurrency cabe en el contenedor de 512 MiB con mensajes de 25 MiB: cada
// composicion ocupa hasta unas cinco veces el tope del mensaje.
const defaultComposeConcurrency = 2

// defaultImageProxyConcurrency son las descargas simultaneas del proxy de imagenes si no se configura.
const defaultImageProxyConcurrency = 8

type Handler struct {
	app     *app.Service
	cfg     Config
	origins *OriginGuard
	logger  *zap.Logger

	// assistant es opcional (SetAssistant): sin el, /assistant responde que no esta disponible.
	assistant *app.AssistantService

	compose *composeGate

	images       *imageGate
	imageMetrics ImageProxyMetrics
}

func NewHandler(svc *app.Service, cfg Config, logger *zap.Logger) (*Handler, error) {
	guard, err := NewOriginGuard(cfg.AllowedOrigins)
	if err != nil {
		return nil, err
	}
	if cfg.OperationTimeout <= 0 || cfg.TransferTimeout <= 0 || cfg.SessionIdle <= 0 || cfg.SessionMax <= 0 ||
		cfg.MaxMessageBytes <= 0 || cfg.ComposeConcurrency < 0 || cfg.MFAChallengeTTL <= 0 {
		return nil, errors.New("webmail: plazos y topes del API deben ser positivos")
	}
	if cfg.IPRateLimiter == nil || cfg.MailboxRateLimiter == nil || cfg.ImageProxyRateLimiter == nil {
		return nil, errors.New("webmail: faltan los limitadores de peticiones por IP, por buzón o del proxy de imágenes")
	}
	if cfg.ComposeConcurrency == 0 {
		cfg.ComposeConcurrency = defaultComposeConcurrency
	}
	if cfg.ImageProxyConcurrency < 0 {
		return nil, errors.New("webmail: las descargas simultáneas del proxy de imágenes no pueden ser negativas")
	}
	if cfg.ImageProxyConcurrency == 0 {
		cfg.ImageProxyConcurrency = defaultImageProxyConcurrency
	}
	metrics := cfg.ImageProxyMetrics
	if metrics == nil {
		metrics = noImageProxyMetrics{}
	}
	return &Handler{app: svc, cfg: cfg, origins: guard, logger: logger,
		compose: newComposeGate(cfg.ComposeConcurrency),
		images:  newImageGate(cfg.ImageProxyConcurrency), imageMetrics: metrics}, nil
}

// PartURL es la URL de una parte en este API. La usa el saneado para las imagenes cid:.
func PartURL(folder string, uid uint32, partID string) string {
	return BasePath + "/folders/" + url.PathEscape(folder) + "/messages/" +
		strconv.FormatUint(uint64(uid), 10) + "/parts/" + url.PathEscape(partID)
}

func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Route(BasePath, func(r chi.Router) {
		r.Use(apiHeaders)
		r.Use(h.origins.Middleware)
		r.With(h.limitByIP).Post("/session", h.Login)
		r.With(h.limitByIP).Post("/session/mfa", h.LoginMFA)
		r.With(h.limitByIP).Delete("/session", h.Logout)
		// El proxy de imagenes remotas no lleva sesion: lo autoriza la firma del enlace.
		r.With(h.limitByIP).Get(ImageProxyPath, h.ImageProxy)
		// El flujo de avisos cuenta una vez por conexion: los latidos no son peticiones.
		r.With(h.requireSessionPeek, h.limitByMailbox).Get("/events", h.Events)
		r.Group(func(r chi.Router) {
			r.Use(h.requireSession)
			r.Use(h.limitByMailbox)
			r.Get("/session", h.Session)
			r.Get("/meta", h.Meta)
			r.Get("/meta/dav", h.DAVMeta)
			r.Get("/identities", h.Identities)
			r.Get("/address-book", h.AddressBook)
			r.Get("/vacation", h.Vacation)
			r.Put("/vacation", h.SetVacation)
			r.Get("/signature", h.Signature)
			r.Put("/signature", h.SetSignature)
			r.Get("/filters", h.Filters)
			r.Put("/filters", h.SetFilters)
			r.Post("/password", h.ChangePassword)
			r.Get("/folders", h.Folders)
			r.Post("/folders", h.CreateFolder)
			r.Patch("/folders/{folder}", h.RenameFolder)
			r.Delete("/folders/{folder}", h.DeleteFolder)
			r.Post("/folders/{folder}/empty", h.EmptyFolder)
			r.Get("/folders/{folder}/messages", h.ListMessages)
			r.Post("/folders/{folder}/messages/batch", h.BatchMessages)
			r.Get("/folders/{folder}/messages/{uid}", h.ReadMessage)
			r.Delete("/folders/{folder}/messages/{uid}", h.DeleteMessage)
			r.Get("/folders/{folder}/messages/{uid}/raw", h.DownloadRaw)
			r.Get("/folders/{folder}/messages/{uid}/parts/{part}", h.DownloadPart)
			r.Post("/folders/{folder}/messages/{uid}/flags", h.ChangeFlags)
			r.Post("/folders/{folder}/messages/{uid}/move", h.MoveMessage)
			r.Post("/send", h.Send)
			r.Post("/drafts", h.SaveDraft)
			r.Get("/scheduled", h.ListScheduled)
			r.Patch("/scheduled/{id}", h.Reschedule)
			r.Delete("/scheduled/{id}", h.CancelScheduled)
			r.Get("/snooze", h.ListSnoozed)
			r.Post("/snooze", h.Snooze)
			r.Patch("/snooze/{id}", h.RescheduleSnooze)
			r.Delete("/snooze/{id}", h.Unsnooze)
			r.Get("/follow-ups", h.ListFollowUps)
			r.Delete("/follow-ups/{id}", h.CancelFollowUp)
			r.Get("/quick-replies", h.QuickReplies)
			r.Post("/quick-replies", h.CreateQuickReply)
			r.Put("/quick-replies/{id}", h.UpdateQuickReply)
			r.Delete("/quick-replies/{id}", h.DeleteQuickReply)
			r.Get("/contacts", h.ListContacts)
			r.Post("/contacts", h.CreateContact)
			r.Get("/contacts/export", h.ExportContacts)
			r.Post("/contacts/import", h.ImportContacts)
			r.Get("/contacts/{id}", h.Contact)
			r.Put("/contacts/{id}", h.UpdateContact)
			r.Delete("/contacts/{id}", h.DeleteContact)
			r.Get("/calendar/events", h.Occurrences)
			r.Post("/calendar/events", h.CreateEvent)
			r.Get("/calendar/events/{id}", h.Event)
			r.Put("/calendar/events/{id}", h.UpdateEvent)
			r.Delete("/calendar/events/{id}", h.DeleteEvent)
			r.Get("/threads", h.Conversation)
			r.Get("/sender-insight", h.SenderInsight)
			r.Post("/unsubscribe", h.Unsubscribe)
			r.Get("/large-files", h.LargeFiles)
			r.Post("/large-files", h.UploadLargeFile)
			r.Delete("/large-files/{id}", h.RevokeLargeFile)
			h.assistantRoutes(r)
			r.Put("/calendar/events/{id}/occurrences/{rid}", h.UpdateOccurrence)
			r.Delete("/calendar/events/{id}/occurrences/{rid}", h.DeleteOccurrence)
			r.Get("/invitations/{folder}/{uid}", h.Invitation)
			r.Post("/invitations/{folder}/{uid}/respond", h.RespondInvitation)
			r.Post("/invitations/{folder}/{uid}/apply", h.ApplyInvitation)
			r.Get("/availability", h.Availability)
			r.Get("/booking", h.BookingSettings)
			r.Put("/booking", h.SaveBookingSettings)
			h.securityRoutes(r)
		})
	})
	// Pagina publica de citas: sin sesion, con las mismas cabeceras del API y el mismo control de origen en la
	// reserva. El gateway la declara como ruta publica con su limite por IP (routes.json).
	r.Route(PublicBookingPath, func(r chi.Router) {
		r.Use(apiHeaders)
		r.Use(h.origins.Middleware)
		r.Use(h.limitByIP)
		r.Get("/{cell}/{tenant}/{page}", h.PublicBooking)
		r.Post("/{cell}/{tenant}/{page}", h.Book)
	})
	return r
}

// apiHeaders: nada del buzon se cachea ni se interpreta como otro tipo.
func apiHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Cache-Control", "no-store")
		hd.Set("Pragma", "no-cache")
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("X-Frame-Options", "DENY")
		hd.Set("Content-Security-Policy", apiCSP)
		hd.Set("Referrer-Policy", "no-referrer")
		hd.Set("Cross-Origin-Resource-Policy", "same-site")
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) opContext(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), h.cfg.OperationTimeout)
}

// fail registra lo que el cliente no puede ver (fallos de infraestructura) y responde. Una sesion
// que ya no sirve se cierra aqui tambien (la cookie y su registro): la interfaz vuelve al inicio de
// sesion.
func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, domain.ErrUnavailable) || errors.Is(err, domain.ErrScanUnavailable) {
		h.logger.Warn("webmail: dependencia no disponible", zap.String("path", r.URL.Path),
			zap.String("request_id", middleware.GetRequestID(r.Context())), zap.Error(err))
	}
	if errors.Is(err, domain.ErrSessionInvalid) {
		if token := cookieValue(r); token != "" {
			ctx, cancel := h.opContext(r)
			if lerr := h.app.Logout(ctx, token); lerr != nil {
				h.logger.Warn("webmail: no se pudo cerrar una sesion invalida", zap.Error(lerr))
			}
			cancel()
		}
		h.clearCookie(w)
	}
	writeError(w, err)
}

// rejectionStatus es el codigo HTTP de cada clase de rechazo de un servicio dueno del dato.
var rejectionStatus = map[domain.RejectionKind]int{
	domain.RejectBadRequest:   http.StatusBadRequest,
	domain.RejectValidation:   http.StatusUnprocessableEntity,
	domain.RejectNotFound:     http.StatusNotFound,
	domain.RejectPrecondition: http.StatusPreconditionFailed,
	domain.RejectTooLarge:     http.StatusRequestEntityTooLarge,
	domain.RejectQuota:        http.StatusInsufficientStorage,
	domain.RejectRateLimited:  http.StatusTooManyRequests,
	domain.RejectUnavailable:  http.StatusServiceUnavailable,
	domain.RejectConflict:     http.StatusConflict,
}

// writeRejection entrega un rechazo de mail-dav tal cual: codigo, mensaje y detalles, con la version
// vigente (ETag) en una precondicion fallida y Retry-After en un cupo o una saturacion.
func writeRejection(w http.ResponseWriter, r *domain.ServiceRejection) {
	status, ok := rejectionStatus[r.Kind]
	if !ok {
		status = http.StatusServiceUnavailable
	}
	if r.ETag != "" && r.Kind == domain.RejectPrecondition {
		w.Header().Set("ETag", r.ETag)
	}
	if r.RetryAfter != "" && (r.Kind == domain.RejectRateLimited || r.Kind == domain.RejectUnavailable) {
		w.Header().Set("Retry-After", r.RetryAfter)
	}
	response.ErrWithDetails(w, status, r.Code, r.Message, r.Details)
}

func writeError(w http.ResponseWriter, err error) {
	var verr *domain.ValidationError
	var rcpt *domain.RecipientRejectedError
	var rejection *domain.ServiceRejection
	var reauth *domain.ReauthRequiredError
	var forbidden *domain.ExternalForwardingDisabledError
	if writeReminderError(w, err) {
		return
	}
	switch {
	case errors.As(err, &reauth):
		writeAddressesError(w, http.StatusForbidden, "REAUTH_REQUIRED", reauth.Error(), reauth.Addresses)
	case errors.As(err, &forbidden):
		writeAddressesError(w, http.StatusUnprocessableEntity, "EXTERNAL_FORWARDING_DISABLED", forbidden.Error(), forbidden.Addresses)
	case errors.Is(err, domain.ErrMFARequired):
		response.Err(w, http.StatusForbidden, "MFA_REQUIRED", domain.ErrMFARequired.Error())
	case errors.Is(err, domain.ErrInvalidMFACode):
		response.Err(w, http.StatusUnprocessableEntity, "INVALID_MFA_CODE", domain.ErrInvalidMFACode.Error())
	case errors.Is(err, domain.ErrMFAChallengeExpired):
		response.Err(w, http.StatusUnauthorized, "MFA_CHALLENGE_EXPIRED", domain.ErrMFAChallengeExpired.Error())
	case errors.Is(err, domain.ErrMFAAlreadyEnabled):
		response.Err(w, http.StatusConflict, "MFA_ALREADY_ENABLED", domain.ErrMFAAlreadyEnabled.Error())
	case errors.Is(err, domain.ErrMFANotEnabled):
		response.Err(w, http.StatusConflict, "MFA_NOT_ENABLED", domain.ErrMFANotEnabled.Error())
	case errors.Is(err, domain.ErrMFASetupExpired):
		response.Err(w, http.StatusConflict, "MFA_SETUP_EXPIRED", domain.ErrMFASetupExpired.Error())
	case errors.Is(err, domain.ErrAppPasswordNotFound):
		response.Err(w, http.StatusNotFound, "APP_PASSWORD_NOT_FOUND", domain.ErrAppPasswordNotFound.Error())
	case errors.Is(err, domain.ErrAppPasswordLimit):
		response.Err(w, http.StatusConflict, "APP_PASSWORD_LIMIT", domain.ErrAppPasswordLimit.Error())
	case errors.As(err, &rejection):
		writeRejection(w, rejection)
	case errors.As(err, &verr):
		response.ErrWithDetails(w, http.StatusUnprocessableEntity, "VALIDATION_ERROR", verr.Error(),
			map[string]string{"field": verr.Field})
	case errors.As(err, &rcpt):
		response.ErrWithDetails(w, http.StatusUnprocessableEntity, "RECIPIENT_REJECTED", rcpt.Error(),
			map[string]string{"address": rcpt.Address})
	case errors.Is(err, domain.ErrComposeBusy):
		w.Header().Set("Retry-After", composeRetryAfter)
		response.Err(w, http.StatusServiceUnavailable, "COMPOSE_BUSY", domain.ErrComposeBusy.Error())
	case errors.Is(err, domain.ErrSendInProgress):
		response.Err(w, http.StatusConflict, "SEND_IN_PROGRESS", domain.ErrSendInProgress.Error())
	case errors.Is(err, domain.ErrDeliveryUncertain):
		response.Err(w, http.StatusConflict, "DELIVERY_UNCERTAIN", domain.ErrDeliveryUncertain.Error())
	case errors.Is(err, domain.ErrIdempotencyKeyReused):
		response.Err(w, http.StatusUnprocessableEntity, "IDEMPOTENCY_KEY_REUSED", domain.ErrIdempotencyKeyReused.Error())
	case errors.Is(err, domain.ErrInvalidCredentials):
		response.Err(w, http.StatusUnauthorized, "INVALID_CREDENTIALS", domain.ErrInvalidCredentials.Error())
	case errors.Is(err, domain.ErrTooManyStreams):
		response.Err(w, http.StatusTooManyRequests, "TOO_MANY_STREAMS", domain.ErrTooManyStreams.Error())
	case errors.Is(err, domain.ErrEventsDisabled):
		response.Err(w, http.StatusServiceUnavailable, "EVENTS_DISABLED", domain.ErrEventsDisabled.Error())
	case errors.Is(err, domain.ErrSessionInvalid):
		response.Err(w, http.StatusUnauthorized, "SESSION_EXPIRED", domain.ErrSessionInvalid.Error())
	case errors.Is(err, domain.ErrFolderNotFound):
		response.Err(w, http.StatusNotFound, "FOLDER_NOT_FOUND", domain.ErrFolderNotFound.Error())
	case errors.Is(err, domain.ErrMessageNotFound):
		response.Err(w, http.StatusNotFound, "MESSAGE_NOT_FOUND", domain.ErrMessageNotFound.Error())
	case errors.Is(err, domain.ErrPartNotFound):
		response.Err(w, http.StatusNotFound, "PART_NOT_FOUND", domain.ErrPartNotFound.Error())
	case errors.Is(err, domain.ErrFolderProtected):
		response.Err(w, http.StatusConflict, "FOLDER_PROTECTED", domain.ErrFolderProtected.Error())
	case errors.Is(err, domain.ErrFolderHasChildren):
		response.Err(w, http.StatusConflict, "FOLDER_HAS_CHILDREN", domain.ErrFolderHasChildren.Error())
	case errors.Is(err, domain.ErrFolderExists):
		response.Err(w, http.StatusConflict, "FOLDER_EXISTS", domain.ErrFolderExists.Error())
	case errors.Is(err, domain.ErrFolderNotEmptiable):
		response.Err(w, http.StatusConflict, "FOLDER_NOT_EMPTIABLE", domain.ErrFolderNotEmptiable.Error())
	case errors.Is(err, domain.ErrScheduledNotFound):
		response.Err(w, http.StatusNotFound, "SCHEDULED_SEND_NOT_FOUND", domain.ErrScheduledNotFound.Error())
	case errors.Is(err, domain.ErrScheduledNotPending):
		response.Err(w, http.StatusConflict, "SCHEDULED_SEND_NOT_PENDING", domain.ErrScheduledNotPending.Error())
	case errors.Is(err, domain.ErrScheduledNotClaimed):
		response.Err(w, http.StatusConflict, "SCHEDULED_SEND_NOT_CLAIMED", domain.ErrScheduledNotClaimed.Error())
	case errors.Is(err, domain.ErrScheduledLimit):
		response.Err(w, http.StatusConflict, "SCHEDULED_SEND_LIMIT", domain.ErrScheduledLimit.Error())
	case errors.Is(err, domain.ErrImportTooLarge):
		response.Err(w, http.StatusRequestEntityTooLarge, "IMPORT_TOO_LARGE", domain.ErrImportTooLarge.Error())
	case errors.Is(err, domain.ErrTrashNotFound):
		response.Err(w, http.StatusConflict, "TRASH_NOT_FOUND", domain.ErrTrashNotFound.Error())
	case errors.Is(err, domain.ErrDraftsNotFound):
		response.Err(w, http.StatusConflict, "DRAFTS_NOT_FOUND", domain.ErrDraftsNotFound.Error())
	case errors.Is(err, domain.ErrQuotaExceeded):
		response.Err(w, http.StatusInsufficientStorage, "QUOTA_EXCEEDED", domain.ErrQuotaExceeded.Error())
	case errors.Is(err, domain.ErrTooManyRecipients):
		response.Err(w, http.StatusUnprocessableEntity, "TOO_MANY_RECIPIENTS", domain.ErrTooManyRecipients.Error())
	case errors.Is(err, domain.ErrMessageTooLarge):
		response.Err(w, http.StatusRequestEntityTooLarge, "MESSAGE_TOO_LARGE", domain.ErrMessageTooLarge.Error())
	case errors.Is(err, domain.ErrPartTooLarge):
		response.Err(w, http.StatusRequestEntityTooLarge, "PART_TOO_LARGE", domain.ErrPartTooLarge.Error())
	case errors.Is(err, domain.ErrSenderNotAllowed):
		response.Err(w, http.StatusForbidden, "SENDER_NOT_ALLOWED", domain.ErrSenderNotAllowed.Error())
	case errors.Is(err, domain.ErrMessageRejected):
		response.Err(w, http.StatusUnprocessableEntity, "MESSAGE_REJECTED", domain.ErrMessageRejected.Error())
	case errors.Is(err, domain.ErrAttachmentInfected):
		response.Err(w, http.StatusUnprocessableEntity, "ATTACHMENT_INFECTED", domain.ErrAttachmentInfected.Error())
	case errors.Is(err, domain.ErrScanUnavailable):
		response.Err(w, http.StatusServiceUnavailable, "SCAN_UNAVAILABLE", domain.ErrScanUnavailable.Error())
	case errors.Is(err, domain.ErrUnsubscribeNotAvailable):
		response.Err(w, http.StatusConflict, "UNSUBSCRIBE_NOT_AVAILABLE", domain.ErrUnsubscribeNotAvailable.Error())
	case errors.Is(err, domain.ErrUnsubscribeRefused):
		response.Err(w, http.StatusUnprocessableEntity, "UNSUBSCRIBE_TARGET_REFUSED", domain.ErrUnsubscribeRefused.Error())
	case errors.Is(err, domain.ErrUnsubscribeFailed):
		response.Err(w, http.StatusBadGateway, "UNSUBSCRIBE_FAILED", domain.ErrUnsubscribeFailed.Error())
	case errors.Is(err, domain.ErrUnavailable):
		response.Err(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", domain.ErrUnavailable.Error())
	default:
		response.Unexpected(w, err)
	}
}

// extendDeadlines amplia los plazos del servidor HTTP para una transferencia grande
// (descarga de un adjunto, subida de un envio). Si el ResponseWriter envuelto no lo
// admite se mantienen los plazos generales del servidor: la transferencia puede cortarse,
// pero nunca queda abierta sin limite.
func extendDeadlines(w http.ResponseWriter, d time.Duration) {
	rc := http.NewResponseController(w)
	deadline := time.Now().Add(d)
	_ = rc.SetReadDeadline(deadline)
	_ = rc.SetWriteDeadline(deadline)
}
