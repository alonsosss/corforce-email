package http

import (
	"net/http"

	"github.com/alonsosss/corforce-email/pkg/response"
)

// internalMailboxResponse es lo minimo que otro servicio de la plataforma necesita saber de un
// buzon: si existe en la empresa, con que nombre entra y si acepta sesion. Nunca lleva la
// contrasena ni los permisos de acceso.
type internalMailboxResponse struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Active   int    `json:"active"`
	Kind     string `json:"kind"`
}

// InternalMailbox la llama mail-migration para comprobar que el buzon destino es de la empresa
// de la peticion y cual es su nombre de inicio de sesion. Ruta interna: la empresa viene de
// X-Tenant-ID y una peticion con usuario no llega (RequireInternalCaller).
func (h *Handler) InternalMailbox(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	m, err := h.uc.GetMailbox(r.Context(), tenantID, id)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, internalMailboxResponse{ID: m.ID.String(), Username: m.Username, Active: m.Active, Kind: m.Kind})
}
