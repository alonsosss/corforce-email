package http

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/contacts/internal/app"
	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/google/uuid"
)

// contactMetaContract escribe a mano los nombres JSON que consume la interfaz.
type contactMetaContract struct {
	Statuses           []string `json:"statuses"`
	ConsentStatuses    []string `json:"consent_statuses"`
	ConsentMethods     []string `json:"consent_methods"`
	APIConsentStatuses []string `json:"api_consent_statuses"`
	APIConsentMethods  []string `json:"api_consent_methods"`
	Sources            []string `json:"sources"`
	AttributeTypes     []string `json:"attribute_types"`
	Import             struct {
		MaxRows            int      `json:"max_rows"`
		MaxErrors          int      `json:"max_errors"`
		ConsentStatuses    []string `json:"consent_statuses"`
		MaxConsentBasisLen int      `json:"max_consent_basis_length"`
	} `json:"import"`
	Limits struct {
		MaxEmailLength          int `json:"max_email_length"`
		MaxNameLength           int `json:"max_name_length"`
		MaxTags                 int `json:"max_tags"`
		MaxTagLength            int `json:"max_tag_length"`
		MaxAttributeDefinitions int `json:"max_attribute_definitions"`
		MaxAttributeStringLen   int `json:"max_attribute_string_length"`
		MaxConsentSourceLength  int `json:"max_consent_source_length"`
		MaxSearchLength         int `json:"max_search_length"`
	} `json:"limits"`
	Pagination struct {
		DefaultPageSize int `json:"default_page_size"`
		MaxPageSize     int `json:"max_page_size"`
	} `json:"pagination"`
}

// /meta no debe caer en /{id}, exige contacts/contacts/read y sus valores salen del
// dominio y de la configuracion efectiva.
func TestContactMetaPublicaElCatalogoDelDominio(t *testing.T) {
	const maxRows = 1234
	uc := app.New(app.Deps{Config: app.Config{ImportMaxRows: maxRows}})
	guard := &recordingGuard{}
	routes := NewHandler(Deps{UC: uc, Perms: guard}).ContactRoutes()

	req := httptest.NewRequest(http.MethodGet, "/meta", nil)
	req = req.WithContext(middleware.WithTenantID(req.Context(), uuid.NewString()))
	rec := httptest.NewRecorder()
	routes.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if want := [3]string{modContacts, "contacts", "read"}; len(guard.seen) != 1 || guard.seen[0] != want {
		t.Errorf("permiso exigido %v, esperado %v", guard.seen, want)
	}
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	var got contactMetaContract
	dec := json.NewDecoder(bytes.NewReader(env.Data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("el cuerpo no cumple el contrato: %v", err)
	}
	checks := []struct {
		name      string
		got, want any
	}{
		{"statuses", got.Statuses, stringsOf(domain.Statuses())},
		{"consent_statuses", got.ConsentStatuses, stringsOf(domain.ConsentStatuses())},
		{"consent_methods", got.ConsentMethods, stringsOf(domain.ConsentMethods())},
		{"api_consent_statuses", got.APIConsentStatuses, stringsOf(domain.GrantStatuses())},
		{"api_consent_methods", got.APIConsentMethods, stringsOf(domain.APIMethods())},
		{"sources", got.Sources, stringsOf(domain.Sources())},
		{"attribute_types", got.AttributeTypes, stringsOf(domain.AttrTypes())},
		{"import.max_rows", got.Import.MaxRows, maxRows},
		{"import.max_errors", got.Import.MaxErrors, app.MaxImportErrors},
		{"import.consent_statuses", got.Import.ConsentStatuses, stringsOf(importConsentStatuses())},
		{"import.max_consent_basis_length", got.Import.MaxConsentBasisLen, app.MaxConsentBasis},
		{"max_name_length", got.Limits.MaxNameLength, domain.MaxNameLength},
		{"max_tags", got.Limits.MaxTags, domain.MaxTags},
		{"max_attribute_definitions", got.Limits.MaxAttributeDefinitions, domain.MaxAttributeDefinitions},
		{"max_attribute_string_length", got.Limits.MaxAttributeStringLen, domain.MaxAttributeString},
		{"max_consent_source_length", got.Limits.MaxConsentSourceLength, domain.MaxConsentSource},
		{"handler limits", [3]int{got.Limits.MaxEmailLength, got.Limits.MaxTagLength, got.Limits.MaxSearchLength}, [3]int{maxEmailLength, maxTagLength, maxSearchLength}},
		{"pagination", [2]int{got.Pagination.DefaultPageSize, got.Pagination.MaxPageSize}, [2]int{defaultPerPage, maxPerPage}},
	}
	for _, c := range checks {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s = %v, quiero %v", c.name, c.got, c.want)
		}
	}
	// Lo que se publica como registrable por API es lo que el dominio acepta.
	for _, s := range got.APIConsentStatuses {
		if _, err := domain.ParseGrantStatus(s); err != nil {
			t.Errorf("estado publicado %q no se acepta: %v", s, err)
		}
	}
	for _, m := range got.APIConsentMethods {
		if _, err := domain.ParseAPIMethod(m); err != nil {
			t.Errorf("metodo publicado %q no se acepta: %v", m, err)
		}
	}
}
