package http

import (
	"errors"
	"net/http"
	"time"

	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
)

const (
	codePrimarySenderLimit = "PRIMARY_SENDER_LIMIT"
	maxPrimarySenderBody   = 1024
)

type primarySenderItem struct {
	Address   string    `json:"address"`
	CreatedAt time.Time `json:"created_at"`
}

type primarySendersResponse struct {
	Items    []primarySenderItem `json:"items"`
	MaxItems int                 `json:"max_items"`
}

type primarySenderRequest struct {
	Address string `json:"address"`
}

func toPrimarySenderItem(s *domain.PrimarySender) primarySenderItem {
	return primarySenderItem{Address: s.Address, CreatedAt: s.CreatedAt.UTC()}
}

func (h *Handler) InternalListPrimarySenders(w http.ResponseWriter, r *http.Request) {
	items, err := h.uc.PrimarySenders(r.Context(), r.URL.Query().Get("username"))
	if err != nil {
		writePrimarySenderError(w, err)
		return
	}
	out := primarySendersResponse{Items: make([]primarySenderItem, 0, len(items)), MaxItems: domain.MaxPrimarySenders}
	for i := range items {
		out.Items = append(out.Items, toPrimarySenderItem(&items[i]))
	}
	response.JSON(w, http.StatusOK, out)
}

func (h *Handler) InternalAddPrimarySender(w http.ResponseWriter, r *http.Request) {
	var req primarySenderRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxPrimarySenderBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	s, err := h.uc.AddPrimarySender(r.Context(), r.URL.Query().Get("username"), req.Address)
	if err != nil {
		writePrimarySenderError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, toPrimarySenderItem(s))
}

func (h *Handler) InternalRemovePrimarySender(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if err := h.uc.RemovePrimarySender(r.Context(), q.Get("username"), q.Get("address")); err != nil {
		writePrimarySenderError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writePrimarySenderError(w http.ResponseWriter, err error) {
	if errors.Is(err, domain.ErrPrimarySenderLimit) {
		response.Err(w, http.StatusConflict, codePrimarySenderLimit, err.Error())
		return
	}
	writeError(w, err)
}
