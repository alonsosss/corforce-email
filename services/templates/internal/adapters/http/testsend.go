package http

import (
	"encoding/json"
	"net/http"

	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/templates/internal/app"
)

type testSendRequest struct {
	From struct {
		Email string `json:"email"`
		Name  string `json:"name,omitempty"`
	} `json:"from"`
	ReplyTo   string                     `json:"reply_to,omitempty"`
	To        []string                   `json:"to"`
	Variables map[string]json.RawMessage `json:"variables,omitempty"`
}

// TestSendVersion envia la version a hasta cinco direcciones de prueba por transactional:
// 202 con los mensajes encolados y las direcciones que la supresion retiro. Los rechazos de
// transactional (remitente sin verificar, tope de pruebas, reputation) llegan con su codigo.
func (h *Handler) TestSendVersion(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	userID, ok := userFrom(w, r)
	if !ok {
		return
	}
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	n, ok := versionParam(w, r)
	if !ok {
		return
	}
	var req testSendRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxRenderBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	v := validate.New()
	v.Required("from.email", req.From.Email)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	out, err := h.uc.SendTest(r.Context(), tenantID, userID, id, n, app.TestSendInput{
		To: req.To, FromEmail: req.From.Email, FromName: req.From.Name, ReplyTo: req.ReplyTo, Variables: req.Variables,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusAccepted, out)
}
