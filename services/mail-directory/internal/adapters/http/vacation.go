package http

import (
	"net/http"
	"time"

	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
)

// maxVacationBody cubre el mensaje mas largo (8192 caracteres, hasta 4 bytes cada uno) y el sobre JSON.
const maxVacationBody = 4*domain.MaxVacationMessageRunes + 4096

type vacationRequest struct {
	Enabled      bool    `json:"enabled"`
	Subject      string  `json:"subject"`
	Message      string  `json:"message"`
	IntervalDays int     `json:"interval_days"`
	StartsOn     *string `json:"starts_on"`
	EndsOn       *string `json:"ends_on"`
}

type vacationResponse struct {
	Enabled      bool       `json:"enabled"`
	Subject      string     `json:"subject"`
	Message      string     `json:"message"`
	IntervalDays int        `json:"interval_days"`
	StartsOn     *string    `json:"starts_on"`
	EndsOn       *string    `json:"ends_on"`
	UpdatedAt    *time.Time `json:"updated_at"`
}

func toVacationResponse(v *domain.VacationReply) vacationResponse {
	out := vacationResponse{
		Enabled: v.Enabled, Subject: v.Subject, Message: v.Message, IntervalDays: v.IntervalDays,
		StartsOn: domain.FormatVacationDate(v.StartsOn), EndsOn: domain.FormatVacationDate(v.EndsOn),
	}
	if !v.UpdatedAt.IsZero() {
		at := v.UpdatedAt.UTC()
		out.UpdatedAt = &at
	}
	return out
}

func (r vacationRequest) toApp() (app.PutVacationRequest, error) {
	var starts, ends *time.Time
	var err error
	if r.StartsOn != nil {
		if starts, err = domain.ParseVacationDate(*r.StartsOn); err != nil {
			return app.PutVacationRequest{}, err
		}
	}
	if r.EndsOn != nil {
		if ends, err = domain.ParseVacationDate(*r.EndsOn); err != nil {
			return app.PutVacationRequest{}, err
		}
	}
	return app.PutVacationRequest{
		Enabled: r.Enabled, Subject: r.Subject, Message: r.Message, IntervalDays: r.IntervalDays,
		StartsOn: starts, EndsOn: ends,
	}, nil
}

// GetVacation y PutVacation: la respuesta automatica de un buzon de la empresa, con los permisos
// de sieve del modulo de buzones.
func (h *Handler) GetVacation(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	v, err := h.uc.GetMailboxVacation(r.Context(), tenantID, id)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, toVacationResponse(v))
}

func (h *Handler) PutVacation(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	var req vacationRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxVacationBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	in, err := req.toApp()
	if err != nil {
		writeError(w, err)
		return
	}
	v, err := h.uc.PutMailboxVacation(r.Context(), tenantID, id, in)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, toVacationResponse(v))
}

// InternalGetVacation e InternalPutVacation los llama el webmail para la respuesta automatica de
// su propio buzon: ruta interna tras RequireGatewayToken, sin empresa, con el buzon en ?username=.
// El webmail toma el buzon de su sesion; el usuario nunca lo elige.
func (h *Handler) InternalGetVacation(w http.ResponseWriter, r *http.Request) {
	v, err := h.uc.VacationByUsername(r.Context(), r.URL.Query().Get("username"))
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, toVacationResponse(v))
}

func (h *Handler) InternalPutVacation(w http.ResponseWriter, r *http.Request) {
	var req vacationRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxVacationBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	in, err := req.toApp()
	if err != nil {
		writeError(w, err)
		return
	}
	v, err := h.uc.PutVacationByUsername(r.Context(), r.URL.Query().Get("username"), in)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, toVacationResponse(v))
}
