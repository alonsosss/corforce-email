package http

import (
	"net/http"
	"time"

	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"github.com/go-chi/chi/v5"
)

type scheduledRefDTO struct {
	ID     string `json:"id"`
	SendAt string `json:"send_at"`
}

type scheduleDTO struct {
	Scheduled scheduledRefDTO `json:"scheduled"`
	// Como en sendDTO: el seguimiento cuenta desde la hora de salida.
	FollowUp      *followUpRefDTO `json:"follow_up,omitempty"`
	FollowUpError string          `json:"follow_up_error,omitempty"`
}

type scheduledDTO struct {
	ID         string   `json:"id"`
	SendAt     string   `json:"send_at"`
	Subject    string   `json:"subject"`
	Recipients []string `json:"recipients"`
	CreatedAt  *string  `json:"created_at"`
	Status     string   `json:"status"`
}

func toScheduledDTO(s domain.ScheduledSend) scheduledDTO {
	return scheduledDTO{
		ID: s.ID, SendAt: formatTime(s.SendAt), Subject: s.Subject, Recipients: nonNil(s.Recipients),
		CreatedAt: formatDate(s.CreatedAt), Status: string(s.Status),
	}
}

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// ListScheduled devuelve los envios programados del buzon de la sesion.
func (h *Handler) ListScheduled(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.opContext(r)
	defer cancel()
	rows, err := h.app.ListScheduled(ctx, sessionFrom(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := make([]scheduledDTO, len(rows))
	for i, row := range rows {
		out[i] = toScheduledDTO(row)
	}
	response.JSON(w, http.StatusOK, out)
}

type rescheduleRequest struct {
	SendAt string `json:"send_at"`
}

// Reschedule cambia la hora de un envio pendiente.
func (h *Handler) Reschedule(w http.ResponseWriter, r *http.Request) {
	var req rescheduleRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxSmallBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	at, err := domain.ParseSendAt(req.SendAt)
	if err != nil {
		writeError(w, err)
		return
	}
	ctx, cancel := h.opContext(r)
	defer cancel()
	row, err := h.app.Reschedule(ctx, sessionFrom(r), chi.URLParam(r, "id"), at)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	response.JSON(w, http.StatusOK, toScheduledDTO(row))
}

// CancelScheduled cancela un envio pendiente; su mensaje vuelve a Borradores.
func (h *Handler) CancelScheduled(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.opContext(r)
	defer cancel()
	if err := h.app.CancelScheduled(ctx, sessionFrom(r), chi.URLParam(r, "id")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
