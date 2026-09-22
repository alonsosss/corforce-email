package http

import (
	"net/http"
	"strconv"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
)

// antispamOrUnavailable devuelve la lectura del antispam o responde 503: sin RSPAMD_CONTROLLER_PASSWORD
// el servicio arranca igual y estas rutas quedan desactivadas.
func (h *Handler) antispamOrUnavailable(w http.ResponseWriter) (*app.AntispamUseCase, bool) {
	if h.antispam == nil {
		writeError(w, domain.ErrNotConfigured)
		return nil, false
	}
	return h.antispam, true
}

// RspamdStats devuelve los contadores del controller de Rspamd de la celda (GET /stat): solo lectura.
func (h *Handler) RspamdStats(w http.ResponseWriter, r *http.Request) {
	uc, ok := h.antispamOrUnavailable(w)
	if !ok {
		return
	}
	out, err := uc.Stats(r.Context(), platformFrom(r), middleware.GetUserID(r.Context()))
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, out)
}

// RspamdHistory devuelve el historial reciente del controller (?limit=, por defecto 50 y como mucho 200):
// sobre y veredicto de cada mensaje, nunca su contenido.
func (h *Handler) RspamdHistory(w http.ResponseWriter, r *http.Request) {
	uc, ok := h.antispamOrUnavailable(w)
	if !ok {
		return
	}
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			response.ErrBadRequest(w, "limit debe ser un entero positivo")
			return
		}
		limit = n
	}
	out, err := uc.History(r.Context(), platformFrom(r), middleware.GetUserID(r.Context()), limit)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, out)
}
