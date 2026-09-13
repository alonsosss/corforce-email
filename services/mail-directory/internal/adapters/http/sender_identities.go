package http

import (
	"net/http"

	"github.com/alonsosss/corforce-email/pkg/response"
)

type senderIdentitiesResponse struct {
	Addresses []string `json:"addresses"`
}

// SenderIdentities la llama el webmail para ofrecer el selector de remitente y para
// validar el remitente elegido: las direcciones concretas que Postfix acepta del buzon
// (smtpd_sender_login_maps). Ruta interna tras RequireGatewayToken, sin empresa.
func (h *Handler) SenderIdentities(w http.ResponseWriter, r *http.Request) {
	addresses, err := h.uc.SenderIdentities(r.Context(), r.URL.Query().Get("username"))
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, senderIdentitiesResponse{Addresses: addresses})
}
