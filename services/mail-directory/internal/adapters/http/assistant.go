package http

import (
	"net/http"
	"time"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// Asistente del webmail (docs/adr/0014): el interruptor por empresa. El panel lo lee y lo cambia con
// los permisos mailboxes/assistant_settings; el webmail lo lee por la ruta interna con el buzon de la
// sesion.

const resourceAssistantSettings = "assistant_settings"

type assistantSettingsRequest struct {
	Enabled *bool `json:"enabled"`
}

type assistantSettingsResponse struct {
	Enabled   bool       `json:"enabled"`
	EnabledAt *time.Time `json:"enabled_at"`
	UpdatedBy *string    `json:"updated_by"`
	UpdatedAt *time.Time `json:"updated_at"`
}

// internalAssistantResponse es lo unico que el webmail necesita saber: si la empresa lo activo.
type internalAssistantResponse struct {
	Enabled bool `json:"enabled"`
}

func toAssistantSettingsResponse(s *domain.AssistantSettings) assistantSettingsResponse {
	out := assistantSettingsResponse{Enabled: s.Enabled, UpdatedAt: updatedAt(s.UpdatedAt)}
	if s.EnabledAt != nil {
		at := s.EnabledAt.UTC()
		out.EnabledAt = &at
	}
	if s.UpdatedBy != nil {
		by := s.UpdatedBy.String()
		out.UpdatedBy = &by
	}
	return out
}

func (h *Handler) assistantRoutes(r chi.Router) {
	r.With(h.require(moduleMailboxes, resourceAssistantSettings, actionRead)).Get("/", h.GetAssistantSettings)
	r.With(h.require(moduleMailboxes, resourceAssistantSettings, actionUpdate)).Put("/", h.SetAssistantSettings)
}

func (h *Handler) GetAssistantSettings(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	s, err := h.uc.AssistantSettings(r.Context(), tenantID)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, toAssistantSettingsResponse(s))
}

// SetAssistantSettings exige enabled explicito: un cuerpo vacio no apaga ni enciende nada por omision.
func (h *Handler) SetAssistantSettings(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	var req assistantSettingsRequest
	if !decode(w, r, &req) {
		return
	}
	if req.Enabled == nil {
		response.ErrWithDetails(w, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "enabled es obligatorio",
			map[string]string{"field": "enabled"})
		return
	}
	by, _ := uuid.Parse(middleware.GetUserID(r.Context()))
	s, err := h.uc.SetAssistantSettings(r.Context(), tenantID, by, *req.Enabled)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, toAssistantSettingsResponse(s))
}

// InternalAssistant es la consulta del webmail, sin empresa y con el buzon de la sesion en ?username=.
func (h *Handler) InternalAssistant(w http.ResponseWriter, r *http.Request) {
	s, err := h.uc.AssistantSettingsByUsername(r.Context(), r.URL.Query().Get("username"))
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, internalAssistantResponse{Enabled: s.Enabled})
}
