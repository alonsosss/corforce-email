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
}

type Handler struct {
	app     *app.Service
	cfg     Config
	origins *OriginGuard
	logger  *zap.Logger
}

func NewHandler(svc *app.Service, cfg Config, logger *zap.Logger) (*Handler, error) {
	guard, err := NewOriginGuard(cfg.AllowedOrigins)
	if err != nil {
		return nil, err
	}
	if cfg.OperationTimeout <= 0 || cfg.TransferTimeout <= 0 || cfg.SessionIdle <= 0 || cfg.SessionMax <= 0 || cfg.MaxMessageBytes <= 0 {
		return nil, errors.New("webmail: plazos y topes del API deben ser positivos")
	}
	return &Handler{app: svc, cfg: cfg, origins: guard, logger: logger}, nil
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
		r.Post("/session", h.Login)
		r.Delete("/session", h.Logout)
		r.Group(func(r chi.Router) {
			r.Use(h.requireSession)
			r.Get("/session", h.Session)
			r.Get("/meta", h.Meta)
			r.Get("/identities", h.Identities)
			r.Get("/folders", h.Folders)
			r.Get("/folders/{folder}/messages", h.ListMessages)
			r.Get("/folders/{folder}/messages/{uid}", h.ReadMessage)
			r.Delete("/folders/{folder}/messages/{uid}", h.DeleteMessage)
			r.Get("/folders/{folder}/messages/{uid}/parts/{part}", h.DownloadPart)
			r.Post("/folders/{folder}/messages/{uid}/flags", h.ChangeFlags)
			r.Post("/folders/{folder}/messages/{uid}/move", h.MoveMessage)
			r.Post("/send", h.Send)
			r.Post("/drafts", h.SaveDraft)
		})
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

// fail registra lo que el cliente no puede ver (fallos de infraestructura) y responde.
func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, domain.ErrUnavailable) || errors.Is(err, domain.ErrScanUnavailable) {
		h.logger.Warn("webmail: dependencia no disponible", zap.String("path", r.URL.Path),
			zap.String("request_id", middleware.GetRequestID(r.Context())), zap.Error(err))
	}
	writeError(w, err)
}

func writeError(w http.ResponseWriter, err error) {
	var verr *domain.ValidationError
	var rcpt *domain.RecipientRejectedError
	switch {
	case errors.As(err, &verr):
		response.ErrWithDetails(w, http.StatusUnprocessableEntity, "VALIDATION_ERROR", verr.Error(),
			map[string]string{"field": verr.Field})
	case errors.As(err, &rcpt):
		response.ErrWithDetails(w, http.StatusUnprocessableEntity, "RECIPIENT_REJECTED", rcpt.Error(),
			map[string]string{"address": rcpt.Address})
	case errors.Is(err, domain.ErrSendInProgress):
		response.Err(w, http.StatusConflict, "SEND_IN_PROGRESS", domain.ErrSendInProgress.Error())
	case errors.Is(err, domain.ErrDeliveryUncertain):
		response.Err(w, http.StatusConflict, "DELIVERY_UNCERTAIN", domain.ErrDeliveryUncertain.Error())
	case errors.Is(err, domain.ErrIdempotencyKeyReused):
		response.Err(w, http.StatusUnprocessableEntity, "IDEMPOTENCY_KEY_REUSED", domain.ErrIdempotencyKeyReused.Error())
	case errors.Is(err, domain.ErrInvalidCredentials):
		response.Err(w, http.StatusUnauthorized, "INVALID_CREDENTIALS", domain.ErrInvalidCredentials.Error())
	case errors.Is(err, domain.ErrSessionInvalid):
		response.Err(w, http.StatusUnauthorized, "SESSION_EXPIRED", domain.ErrSessionInvalid.Error())
	case errors.Is(err, domain.ErrFolderNotFound):
		response.Err(w, http.StatusNotFound, "FOLDER_NOT_FOUND", domain.ErrFolderNotFound.Error())
	case errors.Is(err, domain.ErrMessageNotFound):
		response.Err(w, http.StatusNotFound, "MESSAGE_NOT_FOUND", domain.ErrMessageNotFound.Error())
	case errors.Is(err, domain.ErrPartNotFound):
		response.Err(w, http.StatusNotFound, "PART_NOT_FOUND", domain.ErrPartNotFound.Error())
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
