package http

import (
	"net/http"
	"strconv"

	"github.com/alonsosss/corforce-email/pkg/response"
)

type directoryEntryResponse struct {
	Address     string `json:"address"`
	DisplayName string `json:"display_name"`
}

// InternalSearchDirectory la llama el webmail para la libreta de direcciones de la empresa: ruta
// interna tras RequireGatewayToken, sin empresa, con el buzon de la sesion en ?username=, el texto
// en ?q= y el tope en ?limit= (que el caso de uso acota).
func (h *Handler) InternalSearchDirectory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 0
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			response.ErrBadRequest(w, "limit debe ser un entero positivo")
			return
		}
		limit = n
	}
	entries, err := h.uc.SearchDirectoryByUsername(r.Context(), q.Get("username"), q.Get("q"), limit)
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]directoryEntryResponse, 0, len(entries))
	for _, e := range entries {
		out = append(out, directoryEntryResponse{Address: e.Address, DisplayName: e.DisplayName})
	}
	response.JSON(w, http.StatusOK, out)
}
