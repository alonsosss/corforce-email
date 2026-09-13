package http

import (
	"context"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func TestRutasInternasDeAutomations(t *testing.T) {
	h := NewHandler(Deps{})
	want := map[string]bool{
		"POST /audience":                       false,
		"POST /sendable":                       false,
		"POST /lists/{listID}/members":         false,
		"POST /lists/{listID}/members/remove":  false,
		"POST /lists/{listID}/members/check":   false,
	}
	err := chi.Walk(h.InternalRoutes().(chi.Routes), func(method, route string, _ nethttp.Handler, _ ...func(nethttp.Handler) nethttp.Handler) error {
		key := method + " " + strings.TrimSuffix(route, "/")
		if _, ok := want[key]; ok {
			want[key] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for route, found := range want {
		if !found {
			t.Errorf("falta la ruta interna %s", route)
		}
	}
}

// Los casos fallan antes de llegar al caso de uso: el contrato se fija sin base.
func TestEnviablesValidaElCuerpo(t *testing.T) {
	h := NewHandler(Deps{})
	call := func(tenant, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(nethttp.MethodPost, "/internal/contacts/sendable", strings.NewReader(body))
		if tenant != "" {
			req = req.WithContext(middleware.WithTenantID(context.Background(), tenant))
		}
		rec := httptest.NewRecorder()
		h.Sendable(rec, req)
		return rec
	}
	tenant := uuid.New().String()
	if rec := call("", `{"contact_ids":["`+uuid.New().String()+`"]}`); rec.Code != nethttp.StatusUnauthorized {
		t.Fatalf("sin empresa: %d", rec.Code)
	}
	if rec := call(tenant, `{"contact_ids":[]}`); rec.Code != nethttp.StatusUnprocessableEntity {
		t.Fatalf("sin ids: %d", rec.Code)
	}
	ids := make([]string, 501)
	for i := range ids {
		ids[i] = `"` + uuid.New().String() + `"`
	}
	if rec := call(tenant, `{"contact_ids":[`+strings.Join(ids, ",")+`]}`); rec.Code != nethttp.StatusUnprocessableEntity {
		t.Fatalf("por encima de 500: %d", rec.Code)
	}
	if rec := call(tenant, `{"contact_ids":["`+uuid.New().String()+`"],"list_id":"x"}`); rec.Code != nethttp.StatusBadRequest {
		t.Fatalf("campo fuera del contrato: %d", rec.Code)
	}
}
