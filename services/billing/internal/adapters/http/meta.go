package http

import (
	"net/http"

	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/services/billing/internal/domain"
)

type resourceMeta struct {
	Resource domain.Resource     `json:"resource"`
	Kind     domain.ResourceKind `json:"kind"`
}

type periodMeta struct {
	Period domain.BillingPeriod `json:"period"`
	Months int                  `json:"months"`
}

type subscriptionStatusMeta struct {
	Status domain.SubscriptionStatus `json:"status"`
	// AllowsUsage: con este estado la empresa puede consumir recursos.
	AllowsUsage bool `json:"allows_usage"`
}

type limitsMeta struct {
	MaxPlanNameLength        int   `json:"max_plan_name_length"`
	MaxPlanDescriptionLength int   `json:"max_plan_description_length"`
	PriceScale               int32 `json:"price_scale"`
	UnitPriceScale           int32 `json:"unit_price_scale"`
}

type paginationMeta struct {
	DefaultPageSize int `json:"default_page_size"`
	MaxPageSize     int `json:"max_page_size"`
}

type metaResponse struct {
	Resources            []resourceMeta           `json:"resources"`
	ResourceKinds        []domain.ResourceKind    `json:"resource_kinds"`
	BillingPeriods       []periodMeta             `json:"billing_periods"`
	PlanStatuses         []domain.PlanStatus      `json:"plan_statuses"`
	SubscriptionStatuses []subscriptionStatusMeta `json:"subscription_statuses"`
	// Unlimited en included significa que el plan no limita el recurso.
	Unlimited  int64          `json:"unlimited"`
	Limits     limitsMeta     `json:"limits"`
	Pagination paginationMeta `json:"pagination"`
}

func buildMeta() metaResponse {
	out := metaResponse{
		ResourceKinds: domain.ResourceKinds(),
		PlanStatuses:  domain.PlanStatuses(),
		Unlimited:     domain.Unlimited,
		Limits: limitsMeta{
			MaxPlanNameLength: domain.MaxNameLength, MaxPlanDescriptionLength: domain.MaxDescriptionLength,
			PriceScale: domain.PriceScale, UnitPriceScale: domain.UnitPriceScale,
		},
		Pagination: paginationMeta{DefaultPageSize: defaultPerPage, MaxPageSize: maxPerPage},
	}
	for _, r := range domain.Resources() {
		out.Resources = append(out.Resources, resourceMeta{Resource: r, Kind: r.Kind()})
	}
	for _, p := range domain.BillingPeriods() {
		out.BillingPeriods = append(out.BillingPeriods, periodMeta{Period: p, Months: p.Months()})
	}
	for _, s := range domain.SubscriptionStatuses() {
		out.SubscriptionStatuses = append(out.SubscriptionStatuses, subscriptionStatusMeta{Status: s, AllowsUsage: s.AllowsUsage()})
	}
	return out
}

// Meta publica los recursos con su tipo, periodos, estados y topes: la interfaz construye
// con esto el formulario de plan (un limite por recurso) y los filtros, sin copiarlos.
func (h *Handler) Meta(w http.ResponseWriter, r *http.Request) {
	response.JSON(w, http.StatusOK, buildMeta())
}
