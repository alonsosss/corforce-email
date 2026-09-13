package http

import (
	"net/http"

	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/services/contacts/internal/app"
	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
)

type contactImportMeta struct {
	// MaxRows es el tope efectivo (CONTACTS_IMPORT_MAX_ROWS).
	MaxRows            int                    `json:"max_rows"`
	MaxErrors          int                    `json:"max_errors"`
	ConsentStatuses    []domain.ConsentStatus `json:"consent_statuses"`
	MaxConsentBasisLen int                    `json:"max_consent_basis_length"`
}

type contactLimitsMeta struct {
	MaxEmailLength          int `json:"max_email_length"`
	MaxNameLength           int `json:"max_name_length"`
	MaxTags                 int `json:"max_tags"`
	MaxTagLength            int `json:"max_tag_length"`
	MaxAttributeDefinitions int `json:"max_attribute_definitions"`
	MaxAttributeStringLen   int `json:"max_attribute_string_length"`
	MaxConsentSourceLength  int `json:"max_consent_source_length"`
	MaxSearchLength         int `json:"max_search_length"`
}

type contactPaginationMeta struct {
	DefaultPageSize int `json:"default_page_size"`
	MaxPageSize     int `json:"max_page_size"`
}

type contactMetaResponse struct {
	Statuses        []domain.Status        `json:"statuses"`
	ConsentStatuses []domain.ConsentStatus `json:"consent_statuses"`
	ConsentMethods  []domain.ConsentMethod `json:"consent_methods"`
	// APIConsentStatuses y APIConsentMethods son lo que la empresa puede registrar por
	// POST /contacts/{id}/consent; el alta admite solo el primero de los estados.
	APIConsentStatuses []domain.ConsentStatus `json:"api_consent_statuses"`
	APIConsentMethods  []domain.ConsentMethod `json:"api_consent_methods"`
	Sources            []domain.Source        `json:"sources"`
	AttributeTypes     []domain.AttrType      `json:"attribute_types"`
	Import             contactImportMeta      `json:"import"`
	Limits             contactLimitsMeta      `json:"limits"`
	Pagination         contactPaginationMeta  `json:"pagination"`
}

func buildContactMeta(importMaxRows int) contactMetaResponse {
	return contactMetaResponse{
		Statuses:           domain.Statuses(),
		ConsentStatuses:    domain.ConsentStatuses(),
		ConsentMethods:     domain.ConsentMethods(),
		APIConsentStatuses: domain.GrantStatuses(),
		APIConsentMethods:  domain.APIMethods(),
		Sources:            domain.Sources(),
		AttributeTypes:     domain.AttrTypes(),
		Import: contactImportMeta{
			MaxRows: importMaxRows, MaxErrors: app.MaxImportErrors,
			ConsentStatuses: importConsentStatuses(), MaxConsentBasisLen: app.MaxConsentBasis,
		},
		Limits: contactLimitsMeta{
			MaxEmailLength: maxEmailLength, MaxNameLength: domain.MaxNameLength, MaxTags: domain.MaxTags,
			MaxTagLength: maxTagLength, MaxAttributeDefinitions: domain.MaxAttributeDefinitions,
			MaxAttributeStringLen: domain.MaxAttributeString, MaxConsentSourceLength: domain.MaxConsentSource,
			MaxSearchLength: maxSearchLength,
		},
		Pagination: contactPaginationMeta{DefaultPageSize: defaultPerPage, MaxPageSize: maxPerPage},
	}
}

// ContactMeta publica los valores del dominio que la interfaz necesita para filtros,
// formularios e importacion: estados, metodos de consentimiento, tipos de atributo y
// topes. El editor de segmentos tiene su propio catalogo (GET /segments/meta).
func (h *Handler) ContactMeta(w http.ResponseWriter, r *http.Request) {
	response.JSON(w, http.StatusOK, buildContactMeta(h.uc.ImportMaxRows()))
}
