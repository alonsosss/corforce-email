package http

import (
	"net/http"
	"strconv"

	"github.com/alonsosss/corforce-email/pkg/response"
)

type addressBookEntryDTO struct {
	Address     string `json:"address"`
	DisplayName string `json:"display_name"`
}

// AddressBook busca en la libreta de direcciones de la empresa del buzon de la sesion (?q= y ?limit=).
// La empresa no viaja en la peticion: mail-directory la deduce del buzon.
func (h *Handler) AddressBook(w http.ResponseWriter, r *http.Request) {
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			response.ErrBadRequest(w, "limit debe ser un entero positivo")
			return
		}
		limit = n
	}
	ctx, cancel := h.opContext(r)
	defer cancel()
	entries, err := h.app.SearchAddressBook(ctx, sessionFrom(r), r.URL.Query().Get("q"), limit)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := make([]addressBookEntryDTO, 0, len(entries))
	for _, e := range entries {
		out = append(out, addressBookEntryDTO{Address: e.Address, DisplayName: e.DisplayName})
	}
	response.JSON(w, http.StatusOK, out)
}
