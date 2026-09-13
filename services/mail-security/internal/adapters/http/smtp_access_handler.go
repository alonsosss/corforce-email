package http

import (
	"net/http"

	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/go-chi/chi/v5"
)

// ── Redes SMTP por buzon ──────────────────────────────────────────────────────

func (h *Handler) ListSMTPAccess(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	out, err := h.policy.ListSMTPAccess(r.Context(), tenantID)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, out)
}

func (h *Handler) GetSMTPAccess(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	out, err := h.policy.GetSMTPAccess(r.Context(), tenantID, chi.URLParam(r, "username"))
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, out)
}

// PutSMTPAccess reemplaza las redes del buzon: {"networks": ["203.0.113.0/24", "198.51.100.7"]}.
func (h *Handler) PutSMTPAccess(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	var body struct {
		Networks []string `json:"networks"`
	}
	if !decode(w, r, &body) {
		return
	}
	out, err := h.policy.PutSMTPAccess(r.Context(), tenantID, chi.URLParam(r, "username"), body.Networks)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, out)
}

func (h *Handler) DeleteSMTPAccess(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	if err := h.policy.DeleteSMTPAccess(r.Context(), tenantID, chi.URLParam(r, "username")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
