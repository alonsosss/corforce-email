package http

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/scheduler/internal/domain"
	"github.com/google/uuid"
)

// maxReportBody acota el cierre que manda un ejecutor: el resultado es un resumen de lo
// que hizo, no un volcado.
const maxReportBody = 64 << 10

type completeRequest struct {
	Result json.RawMessage `json:"result"`
}

type failRequest struct {
	Error string `json:"error"`
	// Retryable es obligatorio: un ejecutor que no lo dice no debe quedarse sin reintentos
	// ni reintentar algo que no tiene arreglo por omision.
	Retryable *bool `json:"retryable"`
}

// CompleteExecution: el ejecutor informa que termino con exito. Repetirlo devuelve la misma
// ejecucion; completar una fallida es 409.
func (h *Handler) CompleteExecution(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := reportTarget(w, r)
	if !ok {
		return
	}
	var req completeRequest
	if err := decodeReport(w, r, &req); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	result, err := domain.NormalizeResult(req.Result)
	if err != nil {
		writeError(w, err)
		return
	}
	exec, err := h.uc.CompleteExecution(r.Context(), id, tenantID, result)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, executionResponse(exec))
}

// FailExecution: el ejecutor informa un fallo. Repetirlo devuelve la misma ejecucion sin
// programar otro reintento; fallar una completada es 409.
func (h *Handler) FailExecution(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := reportTarget(w, r)
	if !ok {
		return
	}
	var req failRequest
	if err := decodeReport(w, r, &req); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	v := validate.New()
	v.Required("error", strings.TrimSpace(req.Error))
	if req.Retryable == nil {
		v.Add("retryable", "is required")
	}
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	exec, err := h.uc.FailExecution(r.Context(), id, tenantID, req.Error, *req.Retryable)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, executionResponse(exec))
}

func reportTarget(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	id, err := parseIDParam(r)
	if err != nil {
		response.ErrBadRequest(w, "invalid id")
		return uuid.Nil, uuid.Nil, false
	}
	tenantID, err := parseTenantID(r)
	if err != nil {
		response.ErrUnauthorized(w, "invalid tenant")
		return uuid.Nil, uuid.Nil, false
	}
	return id, tenantID, true
}

// decodeReport acepta un cuerpo vacio como objeto vacio: completar sin resultado no
// necesita cuerpo.
func decodeReport(w http.ResponseWriter, r *http.Request, dst interface{}) error {
	err := validate.DecodeJSONLimit(w, r, dst, maxReportBody)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}
