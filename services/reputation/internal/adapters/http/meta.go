package http

import (
	"net/http"

	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/services/reputation/internal/domain"
)

type limitsMeta struct {
	MaxReasonLength   int   `json:"max_reason_length"`
	MaxAuthorizeCount int64 `json:"max_authorize_count"`
}

type paginationMeta struct {
	DefaultPageSize int `json:"default_page_size"`
	MaxPageSize     int `json:"max_page_size"`
}

type metaResponse struct {
	// States van de menor a mayor gravedad.
	States            []string       `json:"states"`
	Classes           []string       `json:"classes"`
	EvaluationReasons []string       `json:"evaluation_reasons"`
	DenialReasons     []string       `json:"denial_reasons"`
	Limits            limitsMeta     `json:"limits"`
	Pagination        paginationMeta `json:"pagination"`
}

func buildMeta() metaResponse {
	return metaResponse{
		States:            domain.StateNames(),
		Classes:           domain.ClassNames(),
		EvaluationReasons: domain.EvaluationReasons(),
		DenialReasons:     domain.DenialReasons(),
		Limits:            limitsMeta{MaxReasonLength: domain.MaxReasonLength, MaxAuthorizeCount: domain.MaxAuthorizeCount},
		Pagination:        paginationMeta{DefaultPageSize: defaultPerPage, MaxPageSize: maxPerPage},
	}
}

// Meta publica estados, clases de envio, motivos y topes para las pantallas de la empresa
// y del superadmin, sin que la interfaz los copie.
func (h *Handler) Meta(w http.ResponseWriter, r *http.Request) {
	response.JSON(w, http.StatusOK, buildMeta())
}
