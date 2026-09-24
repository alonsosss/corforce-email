package http

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/webmail/internal/app"
	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

// maxQuickReplyBody acota el JSON antes de llegar al directorio, que aplica sus topes y los sirve en
// limits.
const maxQuickReplyBody = 256 << 10

// snoozedDTO es un pospuesto: donde espera (folder, uid) para abrirlo, a donde vuelve y cuando.
type snoozedDTO struct {
	ID           string `json:"id"`
	Folder       string `json:"folder"`
	UID          uint32 `json:"uid"`
	ReturnFolder string `json:"return_folder"`
	Subject      string `json:"subject"`
	From         string `json:"from"`
	Until        string `json:"until"`
	Status       string `json:"status"`
}

func toSnoozedDTO(r domain.Reminder) snoozedDTO {
	d := snoozedDTO{
		ID: r.ID, Folder: r.Folder, UID: r.UID, ReturnFolder: r.ReturnFolder, Subject: r.Subject,
		Until: formatTime(r.DueAt), Status: string(r.Status),
	}
	if len(r.Addresses) > 0 {
		d.From = r.Addresses[0]
	}
	return d
}

type followUpDTO struct {
	ID         string   `json:"id"`
	Subject    string   `json:"subject"`
	Recipients []string `json:"recipients"`
	DueAt      string   `json:"due_at"`
	Status     string   `json:"status"`
}

func toFollowUpDTO(r domain.Reminder) followUpDTO {
	return followUpDTO{ID: r.ID, Subject: r.Subject, Recipients: nonNil(r.Addresses), DueAt: formatTime(r.DueAt), Status: string(r.Status)}
}

// followUpRefDTO acompana a la respuesta de un envio que pidio seguimiento.
type followUpRefDTO struct {
	ID    string `json:"id"`
	DueAt string `json:"due_at"`
}

// followUp registra el seguimiento que pidio un envio (follow_up_days). El mensaje ya salio o quedo
// programado: un fallo aqui no lo deshace, se devuelve su codigo para que la interfaz lo diga.
func (h *Handler) followUp(ctx context.Context, r *http.Request, form composeForm, messageID string, base time.Time) (*followUpRefDTO, string) {
	if form.followUpDays == 0 {
		return nil, ""
	}
	d := form.draft
	var recipients []string
	for _, list := range [][]domain.Address{d.To, d.Cc, d.Bcc} {
		for _, a := range list {
			recipients = append(recipients, a.Email)
		}
	}
	row, err := h.app.CreateFollowUp(ctx, sessionFrom(r), app.FollowUpRequest{
		MessageID: messageID, Subject: d.Subject, Recipients: recipients, Base: base, Days: form.followUpDays,
	})
	if err != nil {
		h.logger.Warn("webmail: envio sin su seguimiento", zap.String("request_id", middleware.GetRequestID(r.Context())), zap.Error(err))
		return nil, followUpErrorCode(err)
	}
	return &followUpRefDTO{ID: row.ID, DueAt: formatTime(row.DueAt)}, ""
}

func followUpErrorCode(err error) string {
	var verr *domain.ValidationError
	if errors.As(err, &verr) {
		return "VALIDATION_ERROR"
	}
	for _, c := range reminderErrorCodes {
		if errors.Is(err, c.err) {
			return c.code
		}
	}
	return "SERVICE_UNAVAILABLE"
}

type snoozeRequest struct {
	Folder string   `json:"folder"`
	UIDs   []uint32 `json:"uids"`
	Until  string   `json:"until"`
}

type snoozeResultDTO struct {
	Snoozed []snoozedDTO `json:"snoozed"`
	Failed  []uint32     `json:"failed"`
}

type untilRequest struct {
	Until string `json:"until"`
}

func parseUntil(raw string) (time.Time, error) {
	at, err := domain.ParseSendAt(raw)
	if err != nil {
		return time.Time{}, domain.NewValidationError("until", "debe ser una fecha y hora RFC 3339")
	}
	return at, nil
}

// Snooze pospone uno o varios mensajes de una carpeta hasta until.
func (h *Handler) Snooze(w http.ResponseWriter, r *http.Request) {
	var req snoozeRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxBatchBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	at, err := parseUntil(req.Until)
	if err != nil {
		writeError(w, err)
		return
	}
	ctx, cancel := h.opContext(r)
	defer cancel()
	res, err := h.app.Snooze(ctx, sessionFrom(r), req.Folder, req.UIDs, at)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := snoozeResultDTO{Snoozed: make([]snoozedDTO, len(res.Snoozed)), Failed: res.Failed}
	for i, s := range res.Snoozed {
		out.Snoozed[i] = toSnoozedDTO(s)
	}
	response.JSON(w, http.StatusOK, out)
}

// ListSnoozed devuelve los pospuestos del buzon con su hora de vuelta.
func (h *Handler) ListSnoozed(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.opContext(r)
	defer cancel()
	rows, err := h.app.ListReminders(ctx, sessionFrom(r), domain.ReminderSnooze)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := make([]snoozedDTO, len(rows))
	for i, row := range rows {
		out[i] = toSnoozedDTO(row)
	}
	response.JSON(w, http.StatusOK, out)
}

// RescheduleSnooze cambia la hora de vuelta de un pospuesto.
func (h *Handler) RescheduleSnooze(w http.ResponseWriter, r *http.Request) {
	var req untilRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxSmallBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	at, err := parseUntil(req.Until)
	if err != nil {
		writeError(w, err)
		return
	}
	ctx, cancel := h.opContext(r)
	defer cancel()
	row, err := h.app.RescheduleReminder(ctx, sessionFrom(r), chi.URLParam(r, "id"), at)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	response.JSON(w, http.StatusOK, toSnoozedDTO(row))
}

// Unsnooze devuelve ya el mensaje a su carpeta.
func (h *Handler) Unsnooze(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.opContext(r)
	defer cancel()
	if err := h.app.Unsnooze(ctx, sessionFrom(r), chi.URLParam(r, "id")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListFollowUps devuelve los seguimientos pendientes del buzon.
func (h *Handler) ListFollowUps(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.opContext(r)
	defer cancel()
	rows, err := h.app.ListReminders(ctx, sessionFrom(r), domain.ReminderFollowUp)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := make([]followUpDTO, len(rows))
	for i, row := range rows {
		out[i] = toFollowUpDTO(row)
	}
	response.JSON(w, http.StatusOK, out)
}

// CancelFollowUp retira un seguimiento.
func (h *Handler) CancelFollowUp(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.opContext(r)
	defer cancel()
	if err := h.app.CancelFollowUp(ctx, sessionFrom(r), chi.URLParam(r, "id")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type quickReplyDTO struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	HTML      string `json:"html"`
	Text      string `json:"text"`
	UpdatedAt string `json:"updated_at"`
}

type quickReplyLimitsDTO struct {
	MaxItems     int `json:"max_items"`
	MaxNameChars int `json:"max_name_chars"`
	MaxHTMLBytes int `json:"max_html_bytes"`
	MaxTextBytes int `json:"max_text_bytes"`
}

type quickRepliesDTO struct {
	Items  []quickReplyDTO     `json:"items"`
	Limits quickReplyLimitsDTO `json:"limits"`
}

type quickReplyRequest struct {
	Name string `json:"name"`
	HTML string `json:"html"`
}

func toQuickReplyDTO(q domain.QuickReply) quickReplyDTO {
	return quickReplyDTO{ID: q.ID, Name: q.Name, HTML: q.HTML, Text: q.Text, UpdatedAt: formatTime(q.UpdatedAt)}
}

func (h *Handler) QuickReplies(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.opContext(r)
	defer cancel()
	list, err := h.app.QuickReplies(ctx, sessionFrom(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := quickRepliesDTO{
		Items: make([]quickReplyDTO, len(list.Items)),
		Limits: quickReplyLimitsDTO{
			MaxItems: list.Limits.MaxItems, MaxNameChars: list.Limits.MaxNameChars,
			MaxHTMLBytes: list.Limits.MaxHTMLBytes, MaxTextBytes: list.Limits.MaxTextBytes,
		},
	}
	for i, q := range list.Items {
		out.Items[i] = toQuickReplyDTO(q)
	}
	response.JSON(w, http.StatusOK, out)
}

// CreateQuickReply guarda una respuesta; el HTML lo sanea el servicio y de el sale el texto.
func (h *Handler) CreateQuickReply(w http.ResponseWriter, r *http.Request) {
	var req quickReplyRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxQuickReplyBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	ctx, cancel := h.opContext(r)
	defer cancel()
	q, err := h.app.CreateQuickReply(ctx, sessionFrom(r), domain.QuickReplyInput{Name: req.Name, HTML: req.HTML})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	response.JSON(w, http.StatusCreated, toQuickReplyDTO(q))
}

func (h *Handler) UpdateQuickReply(w http.ResponseWriter, r *http.Request) {
	var req quickReplyRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxQuickReplyBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	ctx, cancel := h.opContext(r)
	defer cancel()
	q, err := h.app.UpdateQuickReply(ctx, sessionFrom(r), chi.URLParam(r, "id"), domain.QuickReplyInput{Name: req.Name, HTML: req.HTML})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	response.JSON(w, http.StatusOK, toQuickReplyDTO(q))
}

func (h *Handler) DeleteQuickReply(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.opContext(r)
	defer cancel()
	if err := h.app.DeleteQuickReply(ctx, sessionFrom(r), chi.URLParam(r, "id")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// reminderErrorCodes son los codigos de los recordatorios y las respuestas rapidas (writeError).
var reminderErrorCodes = []struct {
	err    error
	status int
	code   string
}{
	{domain.ErrReminderNotFound, http.StatusNotFound, "REMINDER_NOT_FOUND"},
	{domain.ErrReminderNotPending, http.StatusConflict, "REMINDER_NOT_PENDING"},
	{domain.ErrReminderNotClaimed, http.StatusConflict, "REMINDER_NOT_CLAIMED"},
	{domain.ErrReminderLimit, http.StatusConflict, "REMINDER_LIMIT"},
	{domain.ErrReminderExists, http.StatusConflict, "REMINDER_EXISTS"},
	{domain.ErrQuickReplyNotFound, http.StatusNotFound, "QUICK_REPLY_NOT_FOUND"},
	{domain.ErrQuickReplyExists, http.StatusConflict, "QUICK_REPLY_EXISTS"},
	{domain.ErrQuickReplyLimit, http.StatusConflict, "QUICK_REPLY_LIMIT"},
}

// writeReminderError responde los errores de recordatorios y respuestas rapidas; false si err no es
// ninguno de ellos.
func writeReminderError(w http.ResponseWriter, err error) bool {
	for _, c := range reminderErrorCodes {
		if errors.Is(err, c.err) {
			response.Err(w, c.status, c.code, c.err.Error())
			return true
		}
	}
	return false
}
