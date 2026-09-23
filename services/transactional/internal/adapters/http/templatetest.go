package http

import (
	"net/http"

	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/transactional/internal/app"
	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
	"github.com/google/uuid"
)

// maxTemplateTestBody acota la prueba de una plantilla: cinco direcciones y los valores de
// ejemplo de sus variables.
const maxTemplateTestBody = 1 << 20

type templateTestRequest struct {
	TemplateID      *uuid.UUID     `json:"template_id"`
	TemplateVersion *int           `json:"template_version"`
	From            recipientDTO   `json:"from"`
	ReplyTo         string         `json:"reply_to,omitempty"`
	To              []string       `json:"to"`
	Variables       map[string]any `json:"variables,omitempty"`
	RequestedBy     *uuid.UUID     `json:"requested_by,omitempty"`
}

// TemplateTestSend es el contrato que usa templates para la prueba de una version: 202 con
// los mensajes encolados (tambien con todos los destinatarios suprimidos, messages vacio).
func (h *Handler) TemplateTestSend(w http.ResponseWriter, r *http.Request) {
	tenantID, err := uuid.Parse(r.Header.Get("X-Tenant-ID"))
	if err != nil {
		response.ErrUnauthorized(w, "invalid tenant")
		return
	}
	var req templateTestRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxTemplateTestBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	cmd := app.TemplateTestCommand{
		TenantID:    tenantID,
		RequestedBy: req.RequestedBy,
		From:        domain.Recipient(req.From),
		ReplyTo:     req.ReplyTo,
		To:          req.To,
		Variables:   req.Variables,
	}
	if req.TemplateID != nil {
		cmd.TemplateID = *req.TemplateID
	}
	if req.TemplateVersion != nil {
		cmd.TemplateVersion = *req.TemplateVersion
	}
	result, err := h.uc.CreateTemplateTest(r.Context(), cmd)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusAccepted, result)
}
