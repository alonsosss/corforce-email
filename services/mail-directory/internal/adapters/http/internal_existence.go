package http

import (
	"net/http"

	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/app"
	"github.com/google/uuid"
)

// maxExistenceBody acota la peticion: MaxExistenceIDs identificadores de 36 caracteres.
const maxExistenceBody = 64 << 10

type existenceRequest struct {
	IDs []uuid.UUID `json:"ids"`
}

type existenceResponse struct {
	Existing []uuid.UUID `json:"existing"`
}

// InternalMailboxExistence la llaman los servicios que guardan datos por id de buzon (mail-dav,
// mail-migration) para conciliar los de buzones que ya no existen. Devuelve, de los ids pedidos, los que
// son buzones de la empresa de la peticion (X-Tenant-ID); el resto no existe para ella. Ruta interna: una
// peticion con usuario no llega (RequireInternalCaller).
func (h *Handler) InternalMailboxExistence(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	var req existenceRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxExistenceBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	if len(req.IDs) > app.MaxExistenceIDs {
		response.ErrValidation(w, "la consulta supera el maximo de identificadores")
		return
	}
	existing, err := h.uc.ExistingMailboxIDs(r.Context(), tenantID, req.IDs)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, existenceResponse{Existing: existing})
}
