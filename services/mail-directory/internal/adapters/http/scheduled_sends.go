package http

import (
	"net/http"
	"time"

	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
)

// Envios programados (F10). Las rutas de un buzon llevan ?username= (la sesion del webmail); claim y
// finish son del trabajador del webmail y recorren toda la celda.

// maxScheduledBody cubre MaxScheduledRecipients direcciones de hasta 320 bytes con su sobre JSON.
const maxScheduledBody = domain.MaxScheduledRecipients*(domain.MaxScheduledAddressBytes+8) + 16*1024

type createScheduledRequest struct {
	Username    string    `json:"username"`
	MessageID   string    `json:"message_id"`
	Folder      string    `json:"folder"`
	UIDValidity uint32    `json:"uid_validity"`
	UID         uint32    `json:"uid"`
	SendAt      time.Time `json:"send_at"`
	Subject     string    `json:"subject"`
	Recipients  []string  `json:"recipients"`
}

type rescheduleRequest struct {
	SendAt time.Time `json:"send_at"`
}

type claimRequest struct {
	Limit        int `json:"limit"`
	LeaseSeconds int `json:"lease_seconds"`
}

type finishRequest struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Retry  bool   `json:"retry"`
}

func (h *Handler) InternalCreateScheduledSend(w http.ResponseWriter, r *http.Request) {
	var req createScheduledRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxScheduledBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	s, err := h.uc.CreateScheduledSend(r.Context(), app.CreateScheduledSendRequest{
		Username: req.Username, MessageID: req.MessageID, Folder: req.Folder, UIDValidity: req.UIDValidity, UID: req.UID,
		SendAt: req.SendAt, Subject: req.Subject, Recipients: req.Recipients,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, s)
}

func (h *Handler) InternalListScheduledSends(w http.ResponseWriter, r *http.Request) {
	items, err := h.uc.ListScheduledSends(r.Context(), r.URL.Query().Get("username"))
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, items)
}

func (h *Handler) InternalRescheduleSend(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	var req rescheduleRequest
	if err := validate.DecodeJSONLimit(w, r, &req, 4096); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	s, err := h.uc.RescheduleSend(r.Context(), r.URL.Query().Get("username"), id, req.SendAt)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, s)
}

func (h *Handler) InternalCancelScheduledSend(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	if err := h.uc.CancelScheduledSend(r.Context(), r.URL.Query().Get("username"), id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) InternalClaimScheduledSends(w http.ResponseWriter, r *http.Request) {
	var req claimRequest
	if err := validate.DecodeJSONLimit(w, r, &req, 4096); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	items, err := h.uc.ClaimScheduledSends(r.Context(), req.Limit, req.LeaseSeconds)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, items)
}

func (h *Handler) InternalFinishScheduledSend(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	var req finishRequest
	if err := validate.DecodeJSONLimit(w, r, &req, 16*1024); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	s, err := h.uc.FinishScheduledSend(r.Context(), id, domain.ScheduledOutcome{Status: req.Status, Error: req.Error, Retry: req.Retry})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, s)
}
