package http

import (
	"net/http"

	"github.com/alonsosss/corforce-email/pkg/response"
)

// Meta sirve los topes y catalogos con los que la interfaz valida antes de enviar: los
// mismos valores que el servicio aplica, sin copia en el cliente.
func (h *Handler) Meta(w http.ResponseWriter, _ *http.Request) {
	response.JSON(w, http.StatusOK, toMetaDTO(h.app.Meta()))
}

// Identities son los remitentes del buzon para el selector del formulario de redaccion.
func (h *Handler) Identities(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.opContext(r)
	defer cancel()
	identities, err := h.app.Identities(ctx, sessionFrom(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	response.JSON(w, http.StatusOK, toIdentityDTOs(identities))
}
