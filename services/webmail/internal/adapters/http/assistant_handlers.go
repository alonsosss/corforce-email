package http

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/webmail/internal/app"
	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"github.com/go-chi/chi/v5"
)

// Asistente del webmail (docs/adr/0015): /assistant y /assistant/{summarize,reply,tone,extract}. Todo
// lo que devuelve es texto propuesto; nada se envia ni se guarda.

const (
	// maxAssistantRefsBody cubre la lista de mensajes de un resumen de hilo con nombres de carpeta
	// largos.
	maxAssistantRefsBody = 64 << 10
	// assistantRetryAfter es lo que se sugiere esperar cuando el proveedor esta saturado.
	assistantRetryAfter = "30"
	// assistantDeadlineMargin deja escribir la respuesta cuando el plazo de la operacion ya se agoto.
	assistantDeadlineMargin = 5 * time.Second
)

// SetAssistant cablea el asistente. Sin el las rutas responden que no esta disponible.
func (h *Handler) SetAssistant(a *app.AssistantService) { h.assistant = a }

func (h *Handler) assistantRoutes(r chi.Router) {
	r.Get("/assistant", h.AssistantStatus)
	r.Post("/assistant/summarize", h.AssistantSummarize)
	r.Post("/assistant/reply", h.AssistantReply)
	r.Post("/assistant/tone", h.AssistantTone)
	r.Post("/assistant/extract", h.AssistantExtract)
}

type assistantLimitsDTO struct {
	MaxInputChars       int `json:"max_input_chars"`
	MaxThreadMessages   int `json:"max_thread_messages"`
	MaxInstructionChars int `json:"max_instruction_chars"`
	MailboxDaily        int `json:"mailbox_daily"`
	TenantDaily         int `json:"tenant_daily"`
}

type assistantUsageDTO struct {
	Mailbox int `json:"mailbox"`
	Tenant  int `json:"tenant"`
}

type assistantStatusDTO struct {
	Available bool               `json:"available"`
	Reason    string             `json:"reason,omitempty"`
	Actions   []string           `json:"actions"`
	Tones     []string           `json:"tones"`
	Limits    assistantLimitsDTO `json:"limits"`
	Usage     assistantUsageDTO  `json:"usage"`
}

type messageRefDTO struct {
	Folder string `json:"folder"`
	UID    uint32 `json:"uid"`
}

func (d messageRefDTO) toDomain() domain.MessageRef {
	return domain.MessageRef{Folder: d.Folder, UID: d.UID}
}

type assistantSummarizeRequest struct {
	Messages []messageRefDTO `json:"messages"`
}

type assistantReplyRequest struct {
	messageRefDTO
	Instructions string `json:"instructions"`
}

type assistantToneRequest struct {
	Text string `json:"text"`
	Tone string `json:"tone"`
}

type assistantExtractRequest struct {
	messageRefDTO
	// Today es la fecha local del usuario (AAAA-MM-DD) para resolver fechas relativas.
	Today string `json:"today"`
}

type assistantTextDTO struct {
	Text            string `json:"text"`
	InputTruncated  bool   `json:"input_truncated"`
	OutputTruncated bool   `json:"output_truncated"`
}

type assistantExtractionDTO struct {
	Tasks          []domain.ExtractedTask  `json:"tasks"`
	Events         []domain.ExtractedEvent `json:"events"`
	InputTruncated bool                    `json:"input_truncated"`
}

func (h *Handler) AssistantStatus(w http.ResponseWriter, r *http.Request) {
	if h.assistant == nil {
		writeAssistantError(w, domain.ErrAssistantNotConfigured)
		return
	}
	ctx, cancel := h.opContext(r)
	defer cancel()
	st, err := h.assistant.Status(ctx, sessionFrom(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	actions := make([]string, len(domain.AssistantActions))
	for i, a := range domain.AssistantActions {
		actions[i] = string(a)
	}
	tones := make([]string, len(domain.AssistantTones))
	for i, t := range domain.AssistantTones {
		tones[i] = string(t)
	}
	l := st.Limits
	response.JSON(w, http.StatusOK, assistantStatusDTO{
		Available: st.Available, Reason: st.Reason, Actions: actions, Tones: tones,
		Limits: assistantLimitsDTO{
			MaxInputChars: l.MaxInputChars, MaxThreadMessages: l.MaxThreadMessages, MaxInstructionChars: l.MaxInstructionChars,
			MailboxDaily: l.MailboxDaily, TenantDaily: l.TenantDaily,
		},
		Usage: assistantUsageDTO{Mailbox: st.Usage.Mailbox, Tenant: st.Usage.Tenant},
	})
}

func (h *Handler) AssistantSummarize(w http.ResponseWriter, r *http.Request) {
	var req assistantSummarizeRequest
	if !h.decodeAssistant(w, r, &req, maxAssistantRefsBody) {
		return
	}
	refs := make([]domain.MessageRef, len(req.Messages))
	for i, m := range req.Messages {
		refs[i] = m.toDomain()
	}
	h.assistantText(w, r, func(a *app.AssistantService) (domain.AssistantResult, error) {
		ctx, cancel := h.assistantContext(w, r)
		defer cancel()
		return a.Summarize(ctx, sessionFrom(r), refs)
	})
}

func (h *Handler) AssistantReply(w http.ResponseWriter, r *http.Request) {
	var req assistantReplyRequest
	if !h.decodeAssistant(w, r, &req, maxAssistantRefsBody) {
		return
	}
	h.assistantText(w, r, func(a *app.AssistantService) (domain.AssistantResult, error) {
		ctx, cancel := h.assistantContext(w, r)
		defer cancel()
		return a.Reply(ctx, sessionFrom(r), req.toDomain(), req.Instructions)
	})
}

func (h *Handler) AssistantTone(w http.ResponseWriter, r *http.Request) {
	var req assistantToneRequest
	// Un caracter ocupa hasta 4 bytes y el JSON puede escaparlo en 6 (\uXXXX); el tope fino lo aplica
	// el dominio sobre el texto.
	limit := int64(64 << 10)
	if h.assistant != nil {
		limit += int64(h.assistant.Limits().MaxInputChars) * 6
	}
	if !h.decodeAssistant(w, r, &req, limit) {
		return
	}
	tone, err := domain.ParseAssistantTone(req.Tone)
	if err != nil {
		writeError(w, err)
		return
	}
	h.assistantText(w, r, func(a *app.AssistantService) (domain.AssistantResult, error) {
		ctx, cancel := h.assistantContext(w, r)
		defer cancel()
		return a.Tone(ctx, sessionFrom(r), req.Text, tone)
	})
}

func (h *Handler) AssistantExtract(w http.ResponseWriter, r *http.Request) {
	var req assistantExtractRequest
	if !h.decodeAssistant(w, r, &req, maxAssistantRefsBody) {
		return
	}
	if h.assistant == nil {
		writeAssistantError(w, domain.ErrAssistantNotConfigured)
		return
	}
	ctx, cancel := h.assistantContext(w, r)
	defer cancel()
	x, truncated, err := h.assistant.Extract(ctx, sessionFrom(r), req.toDomain(), req.Today)
	if err != nil {
		h.assistantFail(w, r, err)
		return
	}
	response.JSON(w, http.StatusOK, assistantExtractionDTO{Tasks: x.Tasks, Events: x.Events, InputTruncated: truncated})
}

func (h *Handler) decodeAssistant(w http.ResponseWriter, r *http.Request, dst any, limit int64) bool {
	if err := validate.DecodeJSONLimit(w, r, dst, limit); err != nil {
		response.ErrBadRequest(w, err.Error())
		return false
	}
	return true
}

func (h *Handler) assistantText(w http.ResponseWriter, r *http.Request, run func(*app.AssistantService) (domain.AssistantResult, error)) {
	if h.assistant == nil {
		writeAssistantError(w, domain.ErrAssistantNotConfigured)
		return
	}
	res, err := run(h.assistant)
	if err != nil {
		h.assistantFail(w, r, err)
		return
	}
	response.JSON(w, http.StatusOK, assistantTextDTO{Text: res.Text, InputTruncated: res.InputTruncated, OutputTruncated: res.OutputTruncated})
}

// assistantContext amplia el plazo de escritura del servidor: el proveedor puede tardar mas que el
// general, aunque nunca mas que el plazo de una operacion.
func (h *Handler) assistantContext(w http.ResponseWriter, r *http.Request) (context.Context, context.CancelFunc) {
	extendDeadlines(w, h.cfg.OperationTimeout+assistantDeadlineMargin)
	return h.opContext(r)
}

func (h *Handler) assistantFail(w http.ResponseWriter, r *http.Request, err error) {
	if writeAssistantError(w, err) {
		return
	}
	h.fail(w, r, err)
}

// writeAssistantError responde los errores propios del asistente; false si err no es uno de ellos.
func writeAssistantError(w http.ResponseWriter, err error) bool {
	var quota *domain.AssistantQuotaError
	switch {
	case errors.As(err, &quota):
		response.ErrWithDetails(w, http.StatusTooManyRequests, "ASSISTANT_QUOTA_EXCEEDED", quota.Error(),
			map[string]string{"scope": string(quota.Scope), "limit": strconv.Itoa(quota.Limit)})
	case errors.Is(err, domain.ErrAssistantNotConfigured):
		response.Err(w, http.StatusServiceUnavailable, "ASSISTANT_NOT_CONFIGURED", domain.ErrAssistantNotConfigured.Error())
	case errors.Is(err, domain.ErrAssistantDisabled):
		response.Err(w, http.StatusForbidden, "ASSISTANT_DISABLED", domain.ErrAssistantDisabled.Error())
	case errors.Is(err, domain.ErrAssistantBusy):
		w.Header().Set("Retry-After", assistantRetryAfter)
		response.Err(w, http.StatusServiceUnavailable, "ASSISTANT_BUSY", domain.ErrAssistantBusy.Error())
	case errors.Is(err, domain.ErrAssistantRefused):
		response.Err(w, http.StatusUnprocessableEntity, "ASSISTANT_REFUSED", domain.ErrAssistantRefused.Error())
	case errors.Is(err, domain.ErrAssistantEmptyResult):
		response.Err(w, http.StatusBadGateway, "ASSISTANT_EMPTY_RESULT", domain.ErrAssistantEmptyResult.Error())
	case errors.Is(err, domain.ErrAssistantFailed):
		response.Err(w, http.StatusBadGateway, "ASSISTANT_FAILED", domain.ErrAssistantFailed.Error())
	default:
		return false
	}
	return true
}
