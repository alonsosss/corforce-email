package http

import (
	"errors"
	"net/http"
	"time"

	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
)

// Recordatorios (G5 posponer, G6 seguimiento) y respuestas rapidas (G7) del webmail. Como los envios
// programados: las rutas de un buzon llevan ?username= (la sesion del webmail); claim y finish son del
// trabajador del webmail y recorren toda la celda.

const (
	// maxReminderBody cubre MaxReminderAddresses direcciones de hasta 320 bytes con su sobre JSON.
	maxReminderBody   = domain.MaxReminderAddresses*(domain.MaxScheduledAddressBytes+8) + 16*1024
	maxQuickReplyBody = domain.MaxQuickReplyHTMLBytes + domain.MaxQuickReplyTextBytes + 4096

	codeReminderNotPending = "REMINDER_NOT_PENDING"
	codeReminderNotClaimed = "REMINDER_NOT_CLAIMED"
	codeReminderLimit      = "REMINDER_LIMIT"
	codeQuickReplyLimit    = "QUICK_REPLY_LIMIT"
)

type createReminderRequest struct {
	Username     string    `json:"username"`
	Kind         string    `json:"kind"`
	MessageID    string    `json:"message_id"`
	Folder       string    `json:"folder"`
	UIDValidity  uint32    `json:"uid_validity"`
	UID          uint32    `json:"uid"`
	ReturnFolder string    `json:"return_folder"`
	DueAt        time.Time `json:"due_at"`
	Subject      string    `json:"subject"`
	Addresses    []string  `json:"addresses"`
}

type rescheduleReminderRequest struct {
	DueAt time.Time `json:"due_at"`
}

type finishReminderRequest struct {
	Status string `json:"status"`
	Result string `json:"result"`
	Error  string `json:"error"`
	Retry  bool   `json:"retry"`
}

func (h *Handler) InternalCreateReminder(w http.ResponseWriter, r *http.Request) {
	var req createReminderRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxReminderBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	rem, err := h.uc.CreateReminder(r.Context(), app.CreateReminderRequest{
		Username: req.Username, Kind: req.Kind, MessageID: req.MessageID, Folder: req.Folder, UIDValidity: req.UIDValidity,
		UID: req.UID, ReturnFolder: req.ReturnFolder, DueAt: req.DueAt, Subject: req.Subject, Addresses: req.Addresses,
	})
	if err != nil {
		writeReminderError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, rem)
}

func (h *Handler) InternalListReminders(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	items, err := h.uc.ListReminders(r.Context(), q.Get("username"), q.Get("kind"))
	if err != nil {
		writeReminderError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, items)
}

func (h *Handler) InternalRescheduleReminder(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	var req rescheduleReminderRequest
	if err := validate.DecodeJSONLimit(w, r, &req, 4096); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	rem, err := h.uc.RescheduleReminder(r.Context(), r.URL.Query().Get("username"), id, req.DueAt)
	if err != nil {
		writeReminderError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, rem)
}

func (h *Handler) InternalCancelReminder(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	if err := h.uc.CancelReminder(r.Context(), r.URL.Query().Get("username"), id); err != nil {
		writeReminderError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) InternalClaimReminders(w http.ResponseWriter, r *http.Request) {
	var req claimRequest
	if err := validate.DecodeJSONLimit(w, r, &req, 4096); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	items, err := h.uc.ClaimReminders(r.Context(), req.Limit, req.LeaseSeconds)
	if err != nil {
		writeReminderError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, items)
}

func (h *Handler) InternalFinishReminder(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	var req finishReminderRequest
	if err := validate.DecodeJSONLimit(w, r, &req, 16*1024); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	rem, err := h.uc.FinishReminder(r.Context(), id, domain.ReminderOutcome{Status: req.Status, Result: req.Result, Error: req.Error, Retry: req.Retry})
	if err != nil {
		writeReminderError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, rem)
}

// quickReplyItem es lo que el webmail necesita de una respuesta: ni la empresa ni el buzon, que ya conoce.
type quickReplyItem struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	HTML      string    `json:"html"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type quickReplyLimits struct {
	MaxItems     int `json:"max_items"`
	MaxNameChars int `json:"max_name_chars"`
	MaxHTMLBytes int `json:"max_html_bytes"`
	MaxTextBytes int `json:"max_text_bytes"`
}

type quickRepliesResponse struct {
	Items  []quickReplyItem `json:"items"`
	Limits quickReplyLimits `json:"limits"`
}

type quickReplyRequest struct {
	Name string `json:"name"`
	HTML string `json:"html"`
	Text string `json:"text"`
}

func toQuickReplyItem(q *domain.QuickReply) quickReplyItem {
	return quickReplyItem{ID: q.ID, Name: q.Name, HTML: q.HTML, Text: q.Text, CreatedAt: q.CreatedAt.UTC(), UpdatedAt: q.UpdatedAt.UTC()}
}

func (h *Handler) InternalListQuickReplies(w http.ResponseWriter, r *http.Request) {
	items, err := h.uc.QuickReplies(r.Context(), r.URL.Query().Get("username"))
	if err != nil {
		writeReminderError(w, err)
		return
	}
	out := quickRepliesResponse{
		Items: make([]quickReplyItem, 0, len(items)),
		Limits: quickReplyLimits{
			MaxItems: domain.MaxQuickReplies, MaxNameChars: domain.MaxQuickReplyNameRunes,
			MaxHTMLBytes: domain.MaxQuickReplyHTMLBytes, MaxTextBytes: domain.MaxQuickReplyTextBytes,
		},
	}
	for i := range items {
		out.Items = append(out.Items, toQuickReplyItem(&items[i]))
	}
	response.JSON(w, http.StatusOK, out)
}

func (h *Handler) InternalCreateQuickReply(w http.ResponseWriter, r *http.Request) {
	var req quickReplyRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxQuickReplyBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	q, err := h.uc.CreateQuickReply(r.Context(), r.URL.Query().Get("username"), app.QuickReplyInput{Name: req.Name, HTML: req.HTML, Text: req.Text})
	if err != nil {
		writeReminderError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, toQuickReplyItem(q))
}

func (h *Handler) InternalUpdateQuickReply(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	var req quickReplyRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxQuickReplyBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	q, err := h.uc.UpdateQuickReply(r.Context(), r.URL.Query().Get("username"), id, app.QuickReplyInput{Name: req.Name, HTML: req.HTML, Text: req.Text})
	if err != nil {
		writeReminderError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, toQuickReplyItem(q))
}

func (h *Handler) InternalDeleteQuickReply(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	if err := h.uc.DeleteQuickReply(r.Context(), r.URL.Query().Get("username"), id); err != nil {
		writeReminderError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeReminderError da su codigo a los choques de recordatorios y respuestas rapidas; el resto sigue
// la traduccion comun.
func writeReminderError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrReminderNotPending):
		response.Err(w, http.StatusConflict, codeReminderNotPending, err.Error())
	case errors.Is(err, domain.ErrReminderNotClaimed):
		response.Err(w, http.StatusConflict, codeReminderNotClaimed, err.Error())
	case errors.Is(err, domain.ErrReminderLimit):
		response.Err(w, http.StatusConflict, codeReminderLimit, err.Error())
	case errors.Is(err, domain.ErrQuickReplyLimit):
		response.Err(w, http.StatusConflict, codeQuickReplyLimit, err.Error())
	default:
		writeError(w, err)
	}
}
