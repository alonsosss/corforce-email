package http

import (
	"net/http"

	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

// maxVacationBody cubre el mensaje mas largo que admite el directorio (8192 caracteres, hasta 4
// bytes cada uno) y el sobre JSON; el directorio vuelve a validar el texto.
const maxVacationBody = 64 << 10

// Vacation devuelve la respuesta automatica del buzon de la sesion, con los topes que aplica el
// directorio para que la interfaz no los copie.
func (h *Handler) Vacation(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.opContext(r)
	defer cancel()
	v, err := h.app.Vacation(ctx, sessionFrom(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	response.JSON(w, http.StatusOK, toVacationDTO(v))
}

// SetVacation reemplaza la respuesta automatica del buzon de la sesion. El buzon no viaja en la
// peticion: sale de la sesion. Toda escritura exige un Origin permitido (OriginGuard).
func (h *Handler) SetVacation(w http.ResponseWriter, r *http.Request) {
	var req vacationRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxVacationBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	ctx, cancel := h.opContext(r)
	defer cancel()
	v, err := h.app.SetVacation(ctx, sessionFrom(r), domain.VacationInput{
		Enabled: req.Enabled, Subject: req.Subject, Message: req.Message, IntervalDays: req.IntervalDays,
		StartsOn: req.StartsOn, EndsOn: req.EndsOn,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	response.JSON(w, http.StatusOK, toVacationDTO(v))
}
