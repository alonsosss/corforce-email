package http

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
)

// queueOrUnavailable devuelve el gestor de la cola o responde 503: sin QUEUE_AGENT_API_KEY el servicio
// arranca igual y estas rutas quedan desactivadas.
func (h *Handler) queueOrUnavailable(w http.ResponseWriter) (*app.QueueUseCase, bool) {
	if h.queue == nil {
		writeError(w, domain.ErrNotConfigured)
		return nil, false
	}
	return h.queue, true
}

// ListQueue devuelve la cola de Postfix de la celda (?limit=, por defecto 100 y como mucho 500). No
// devuelve el contenido de los mensajes, solo su remitente, destinatarios y el motivo del diferimiento.
func (h *Handler) ListQueue(w http.ResponseWriter, r *http.Request) {
	uc, ok := h.queueOrUnavailable(w)
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
	out, err := uc.List(r.Context(), platformFrom(r), limit)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, out)
}

// queueAction es el manejador de una accion sobre un mensaje: retry, hold, unhold (POST) o delete (DELETE).
func queueAction(action domain.QueueAction) func(*Handler, http.ResponseWriter, *http.Request) {
	return func(h *Handler, w http.ResponseWriter, r *http.Request) {
		uc, ok := h.queueOrUnavailable(w)
		if !ok {
			return
		}
		if err := uc.Apply(r.Context(), platformFrom(r), middleware.GetUserID(r.Context()), action, chi.URLParam(r, "id")); err != nil {
			writeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// FlushQueue pide a Postfix reintentar toda la cola diferida. 202: la entrega ocurre despues.
func (h *Handler) FlushQueue(w http.ResponseWriter, r *http.Request) {
	uc, ok := h.queueOrUnavailable(w)
	if !ok {
		return
	}
	if err := uc.Flush(r.Context(), platformFrom(r), middleware.GetUserID(r.Context())); err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
}
