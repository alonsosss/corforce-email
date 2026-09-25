package http

import (
	"errors"
	"net/http"
	"time"

	"github.com/alonsosss/corforce-email/pkg/rawmail"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/transactional/internal/app"
	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
	"github.com/google/uuid"
)

// maxRawBody acota el JSON del mensaje crudo: el MIME viaja en base64 (4/3) mas el sobre.
const maxRawBody = int64(domain.MaxRawBytes)/3*4 + 1<<20

// rawMessageRequest es el contrato de POST /internal/transactional/raw-messages, que usa
// smtp-relay al terminar el DATA de una sesion autenticada. La empresa va en X-Tenant-ID.
type rawMessageRequest struct {
	EnvelopeFrom   string     `json:"envelope_from"`
	Recipients     []string   `json:"recipients"`
	Raw            []byte     `json:"raw"`
	IdempotencyKey string     `json:"idempotency_key"`
	APIKeyID       *uuid.UUID `json:"api_key_id,omitempty"`
}

// InternalRawMessage acepta un mensaje MIME con las mismas reglas que POST /messages: 202 al
// encolar, 200 si todos los destinatarios estaban suprimidos o la clave de idempotencia ya
// existia, 422 con un codigo propio si el MIME no se admite.
func (h *Handler) InternalRawMessage(w http.ResponseWriter, r *http.Request) {
	tenantID, err := uuid.Parse(r.Header.Get("X-Tenant-ID"))
	if err != nil {
		response.ErrUnauthorized(w, "invalid tenant")
		return
	}
	var req rawMessageRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxRawBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	parsed, err := rawmail.Parse(req.Raw, rawmail.Limits{MaxBytes: domain.MaxRawBytes})
	if err != nil {
		writeRawError(w, err)
		return
	}
	clean, err := rawmail.Sanitize(req.Raw, time.Now())
	if err != nil {
		writeRawError(w, err)
		return
	}
	cmd := app.RawMessageCommand{
		TenantID:       tenantID,
		APIKeyID:       req.APIKeyID,
		IdempotencyKey: req.IdempotencyKey,
		EnvelopeFrom:   req.EnvelopeFrom,
		Recipients:     req.Recipients,
		From:           domain.Recipient{Email: parsed.From.Email, Name: parsed.From.Name},
		ReplyTo:        parsed.ReplyTo,
		Subject:        parsed.Subject,
		Text:           parsed.Text,
		HTML:           parsed.HTML,
		Raw:            clean,
		Bulk:           parsed.Bulk,
	}
	if parsed.Sender != nil {
		cmd.Sender = parsed.Sender.Email
	}
	result, err := h.uc.CreateRawMessage(r.Context(), cmd)
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

func writeRawError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, rawmail.ErrTooLarge):
		response.Err(w, http.StatusRequestEntityTooLarge, "MESSAGE_TOO_LARGE", "el mensaje supera el tamaño máximo")
	case errors.Is(err, rawmail.ErrFrom):
		response.Err(w, http.StatusUnprocessableEntity, "MESSAGE_FROM_INVALID", "el mensaje debe llevar un único remitente válido en From")
	case errors.Is(err, rawmail.ErrTooManyParts):
		response.Err(w, http.StatusUnprocessableEntity, "MESSAGE_TOO_MANY_PARTS", "el mensaje supera el número de partes MIME")
	case errors.Is(err, rawmail.ErrLineTooLong):
		response.Err(w, http.StatusUnprocessableEntity, "MESSAGE_LINE_TOO_LONG", "una línea del mensaje supera 998 octetos")
	default:
		response.Err(w, http.StatusUnprocessableEntity, "MESSAGE_MALFORMED", "el mensaje MIME no es válido")
	}
}
