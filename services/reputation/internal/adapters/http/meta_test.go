package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/reputation/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// access-control inalcanzable: solo un rol del sistema pasa sin consultarlo.
const unreachableAccessControl = "http://127.0.0.1:9"

type metaContract struct {
	States            []string `json:"states"`
	Classes           []string `json:"classes"`
	EvaluationReasons []string `json:"evaluation_reasons"`
	DenialReasons     []string `json:"denial_reasons"`
	Limits            struct {
		MaxReasonLength   int   `json:"max_reason_length"`
		MaxAuthorizeCount int64 `json:"max_authorize_count"`
	} `json:"limits"`
	Pagination struct {
		DefaultPageSize int `json:"default_page_size"`
		MaxPageSize     int `json:"max_page_size"`
	} `json:"pagination"`
}

// router monta el catalogo como main: aparte de /api/v1/reputation, que lleva el pool de
// la empresa, y de /tenants, que exige superadmin.
func router() http.Handler {
	h := NewHandler(nil, authz.NewChecker(unreachableAccessControl, ""))
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(func(http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "pool de empresa", http.StatusTeapot)
			})
		})
		r.Mount("/api/v1/reputation", h.PublicRoutes())
	})
	r.Mount("/api/v1/reputation/meta", h.MetaRoutes())
	r.Mount("/api/v1/reputation/tenants", h.PlatformRoutes())
	return r
}

func metaRequest(roles ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/reputation/meta", nil)
	ctx := middleware.WithTenantID(req.Context(), uuid.NewString())
	ctx = context.WithValue(ctx, middleware.CtxUserID, uuid.NewString())
	ctx = context.WithValue(ctx, middleware.CtxRoles, roles)
	rec := httptest.NewRecorder()
	router().ServeHTTP(rec, req.WithContext(ctx))
	return rec
}

// El catalogo no pasa por el pool de la empresa (el superadmin no tiene base) ni por la
// ruta de plataforma (la empresa tambien lo lee).
func TestMetaSeSirveSinPoolDeEmpresaNiRolDePlataforma(t *testing.T) {
	for _, role := range []string{middleware.RoleTenantAdmin, middleware.RoleSuperadmin} {
		rec := metaRequest(role)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d: %s", role, rec.Code, rec.Body)
		}
	}
	if rec := metaRequest("editor"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("sin politica comprobable debe fallar cerrado: status %d", rec.Code)
	}
}

func TestMetaPublicaElCatalogoDelDominio(t *testing.T) {
	rec := metaRequest(middleware.RoleTenantAdmin)
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	var got metaContract
	dec := json.NewDecoder(bytes.NewReader(env.Data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("el cuerpo no cumple el contrato: %v", err)
	}
	checks := []struct {
		name      string
		got, want any
	}{
		{"states", got.States, domain.StateNames()},
		{"classes", got.Classes, domain.ClassNames()},
		{"evaluation_reasons", got.EvaluationReasons, domain.EvaluationReasons()},
		{"denial_reasons", got.DenialReasons, domain.DenialReasons()},
		{"max_reason_length", got.Limits.MaxReasonLength, domain.MaxReasonLength},
		{"max_authorize_count", got.Limits.MaxAuthorizeCount, domain.MaxAuthorizeCount},
		{"pagination", [2]int{got.Pagination.DefaultPageSize, got.Pagination.MaxPageSize}, [2]int{defaultPerPage, maxPerPage}},
	}
	for _, c := range checks {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s = %v, quiero %v", c.name, c.got, c.want)
		}
	}
	for _, s := range got.States {
		if _, err := domain.ParseState(s); err != nil {
			t.Errorf("estado publicado %q no se acepta", s)
		}
	}
	for _, c := range got.Classes {
		if _, err := domain.ParseClass(c); err != nil {
			t.Errorf("clase publicada %q no se acepta", c)
		}
	}
}
