package http

import (
	"errors"
	"net/http"

	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/organization/internal/app"
	"github.com/alonsosss/corforce-email/services/organization/internal/domain"
)

// maxTCPPort es el puerto mas alto direccionable; sirve solo para acotar la entrada.
const maxTCPPort = 65535

type createCellRequest struct {
	Code   string `json:"code"`
	Region string `json:"region"`
	DBHost string `json:"db_host"`
	DBPort int    `json:"db_port"`
}

func (h *Handler) CreateCell(w http.ResponseWriter, r *http.Request) {
	var req createCellRequest
	if err := validate.DecodeJSON(r, &req); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	v := validate.New()
	v.Required("code", req.Code)
	v.MaxLength("code", req.Code, 63)
	v.Required("region", req.Region)
	v.MaxLength("region", req.Region, 63)
	v.Required("db_host", req.DBHost)
	v.MaxLength("db_host", req.DBHost, 255)
	if req.DBPort < 1 || req.DBPort > maxTCPPort {
		v.Add("db_port", "db_port debe estar entre 1 y 65535")
	}
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}

	cell, err := h.uc.CreateCell(r.Context(), app.CreateCellRequest{
		Code: req.Code, Region: req.Region, DBHost: req.DBHost, DBPort: req.DBPort,
	})
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrCellCodeExists):
			response.ErrConflict(w, err.Error())
		case errors.Is(err, domain.ErrInvalidCellCode):
			response.ErrValidation(w, err.Error())
		default:
			response.Unexpected(w, err)
		}
		return
	}
	response.JSON(w, http.StatusCreated, cellResponse(cell))
}

func (h *Handler) ListCells(w http.ResponseWriter, r *http.Request) {
	cells, err := h.uc.ListCells(r.Context())
	if err != nil {
		response.Unexpected(w, err)
		return
	}
	items := make([]map[string]interface{}, 0, len(cells))
	for _, c := range cells {
		items = append(items, cellResponse(c))
	}
	response.JSON(w, http.StatusOK, items)
}

type updateCellRequest struct {
	Status *string `json:"status,omitempty"`
	Region *string `json:"region,omitempty"`
}

func (h *Handler) UpdateCell(w http.ResponseWriter, r *http.Request) {
	id, ok := parseIDParam(w, r, "celda")
	if !ok {
		return
	}
	var req updateCellRequest
	if err := validate.DecodeJSON(r, &req); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	v := validate.New()
	if req.Status != nil {
		v.Required("status", *req.Status)
		v.OneOf("status", *req.Status, domain.CellStatuses())
	}
	if req.Region != nil {
		v.Required("region", *req.Region)
		v.MaxLength("region", *req.Region, 63)
	}
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}

	cell, err := h.uc.UpdateCell(r.Context(), id, app.UpdateCellRequest{Status: req.Status, Region: req.Region})
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrCellNotFound):
			response.ErrNotFound(w, err.Error())
		case errors.Is(err, domain.ErrNothingToUpdate), errors.Is(err, domain.ErrInvalidCellStatus):
			response.ErrValidation(w, err.Error())
		default:
			response.Unexpected(w, err)
		}
		return
	}
	response.JSON(w, http.StatusOK, cellResponse(cell))
}

func cellResponse(c *domain.Cell) map[string]interface{} {
	return map[string]interface{}{
		"id":         c.ID.String(),
		"code":       c.Code,
		"region":     c.Region,
		"status":     c.Status,
		"db_host":    c.DBHost,
		"db_port":    c.DBPort,
		"created_at": c.CreatedAt,
	}
}
