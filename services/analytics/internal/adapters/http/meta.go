package http

import (
	"net/http"

	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/services/analytics/internal/domain"
)

type metaRange struct {
	MaxDays     int `json:"max_days"`
	DefaultDays int `json:"default_days"`
}

type metaDomains struct {
	DefaultLimit int `json:"default_limit"`
	MaxLimit     int `json:"max_limit"`
}

type metaPagination struct {
	DefaultPerPage int `json:"default_per_page"`
	MaxPerPage     int `json:"max_per_page"`
}

type metaResponse struct {
	Classes    []string       `json:"classes"`
	Timezone   string         `json:"timezone"`
	Range      metaRange      `json:"range"`
	Domains    metaDomains    `json:"domains"`
	Pagination metaPagination `json:"pagination"`
}

// Meta es el catalogo de la analitica para la UI: clases de envio, zona de los dias y
// limites de las consultas, tomados de las constantes del dominio.
func (h *Handler) Meta(w http.ResponseWriter, r *http.Request) {
	response.JSON(w, http.StatusOK, buildMeta())
}

func buildMeta() metaResponse {
	classes := make([]string, 0, len(domain.Classes()))
	for _, c := range domain.Classes() {
		classes = append(classes, string(c))
	}
	return metaResponse{
		Classes:    classes,
		Timezone:   domain.ReportTimezone,
		Range:      metaRange{MaxDays: domain.MaxRangeDays, DefaultDays: domain.DefaultRangeDays},
		Domains:    metaDomains{DefaultLimit: domain.DefaultDomainLimit, MaxLimit: domain.MaxDomainLimit},
		Pagination: metaPagination{DefaultPerPage: defaultPerPage, MaxPerPage: maxPerPage},
	}
}
