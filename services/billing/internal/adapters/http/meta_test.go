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
	"github.com/alonsosss/corforce-email/services/billing/internal/domain"
	"github.com/google/uuid"
)

// access-control inalcanzable: solo un rol del sistema pasa sin consultarlo.
const unreachableAccessControl = "http://127.0.0.1:9"

// metaContract escribe a mano los nombres JSON que consume la interfaz.
type metaContract struct {
	Resources []struct {
		Resource string `json:"resource"`
		Kind     string `json:"kind"`
	} `json:"resources"`
	ResourceKinds  []string `json:"resource_kinds"`
	BillingPeriods []struct {
		Period string `json:"period"`
		Months int    `json:"months"`
	} `json:"billing_periods"`
	PlanStatuses         []string `json:"plan_statuses"`
	SubscriptionStatuses []struct {
		Status      string `json:"status"`
		AllowsUsage bool   `json:"allows_usage"`
	} `json:"subscription_statuses"`
	Unlimited int64 `json:"unlimited"`
	Limits    struct {
		MaxPlanNameLength        int   `json:"max_plan_name_length"`
		MaxPlanDescriptionLength int   `json:"max_plan_description_length"`
		PriceScale               int32 `json:"price_scale"`
		UnitPriceScale           int32 `json:"unit_price_scale"`
	} `json:"limits"`
	Pagination struct {
		DefaultPageSize int `json:"default_page_size"`
		MaxPageSize     int `json:"max_page_size"`
	} `json:"pagination"`
}

func metaRequest(roles ...string) *httptest.ResponseRecorder {
	h := NewHandler(nil, authz.NewChecker(unreachableAccessControl, ""))
	req := httptest.NewRequest(http.MethodGet, "/meta", nil)
	ctx := middleware.WithTenantID(req.Context(), uuid.NewString())
	ctx = context.WithValue(ctx, middleware.CtxUserID, uuid.NewString())
	ctx = context.WithValue(ctx, middleware.CtxRoles, roles)
	rec := httptest.NewRecorder()
	h.PublicRoutes().ServeHTTP(rec, req.WithContext(ctx))
	return rec
}

// La empresa lee el catalogo (no es una ruta de plataforma) y sus valores salen del dominio.
func TestMetaPublicaElCatalogoDelDominio(t *testing.T) {
	rec := metaRequest(middleware.RoleTenantAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
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
	var resources, periods, planStatuses, subStatuses []string
	for _, r := range got.Resources {
		resources = append(resources, r.Resource)
		if want := domain.Resource(r.Resource).Kind(); r.Kind != string(want) {
			t.Errorf("%s: tipo %s, dominio %s", r.Resource, r.Kind, want)
		}
	}
	if !reflect.DeepEqual(resources, strs(domain.Resources())) {
		t.Errorf("resources %v, dominio %v", resources, domain.Resources())
	}
	for _, p := range got.BillingPeriods {
		periods = append(periods, p.Period)
		if p.Months != domain.BillingPeriod(p.Period).Months() {
			t.Errorf("%s: %d meses", p.Period, p.Months)
		}
		if _, err := domain.ParseBillingPeriod(p.Period); err != nil {
			t.Errorf("periodo publicado %q no se acepta", p.Period)
		}
	}
	for _, s := range got.SubscriptionStatuses {
		subStatuses = append(subStatuses, s.Status)
		if s.AllowsUsage != domain.SubscriptionStatus(s.Status).AllowsUsage() {
			t.Errorf("%s: allows_usage", s.Status)
		}
	}
	planStatuses = got.PlanStatuses
	checks := []struct {
		name      string
		got, want any
	}{
		{"resource_kinds", got.ResourceKinds, strs(domain.ResourceKinds())},
		{"billing_periods", periods, strs(domain.BillingPeriods())},
		{"plan_statuses", planStatuses, strs(domain.PlanStatuses())},
		{"subscription_statuses", subStatuses, strs(domain.SubscriptionStatuses())},
		{"unlimited", got.Unlimited, domain.Unlimited},
		{"limits", [2]int{got.Limits.MaxPlanNameLength, got.Limits.MaxPlanDescriptionLength}, [2]int{domain.MaxNameLength, domain.MaxDescriptionLength}},
		{"scales", [2]int32{got.Limits.PriceScale, got.Limits.UnitPriceScale}, [2]int32{domain.PriceScale, domain.UnitPriceScale}},
		{"pagination", [2]int{got.Pagination.DefaultPageSize, got.Pagination.MaxPageSize}, [2]int{defaultPerPage, maxPerPage}},
	}
	for _, c := range checks {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s = %v, quiero %v", c.name, c.got, c.want)
		}
	}
}

func strs[T ~string](in []T) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = string(v)
	}
	return out
}

func TestMetaExigeElPermisoDeLectura(t *testing.T) {
	if rec := metaRequest("editor"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("sin politica comprobable debe fallar cerrado: status %d", rec.Code)
	}
}
