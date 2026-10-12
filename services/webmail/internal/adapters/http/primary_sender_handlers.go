package http

import (
	"net/http"

	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
)

// maxPrimarySenderBody acota el JSON: lleva una sola direccion.
const maxPrimarySenderBody = 1024

type primarySendersDTO struct {
	Items []string `json:"items"`
}

type primarySenderRequest struct {
	Address string `json:"address"`
}

func (h *Handler) PrimarySenders(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.opContext(r)
	defer cancel()
	list, err := h.app.PrimarySenders(ctx, sessionFrom(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if list == nil {
		list = []string{}
	}
	response.JSON(w, http.StatusOK, primarySendersDTO{Items: list})
}

// AddPrimarySender fija un remitente en Principal. PUT: repetirlo deja el mismo estado.
func (h *Handler) AddPrimarySender(w http.ResponseWriter, r *http.Request) {
	var req primarySenderRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxPrimarySenderBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	ctx, cancel := h.opContext(r)
	defer cancel()
	if err := h.app.AddPrimarySender(ctx, sessionFrom(r), req.Address); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RemovePrimarySender quita un remitente de Principal; la direccion va en la query para no
// codificarla en la ruta.
func (h *Handler) RemovePrimarySender(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.opContext(r)
	defer cancel()
	if err := h.app.RemovePrimarySender(ctx, sessionFrom(r), r.URL.Query().Get("address")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
