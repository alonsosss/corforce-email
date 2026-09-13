package http

import (
	"encoding/json"
	"time"

	"github.com/alonsosss/corforce-email/services/billing/internal/domain"
	"github.com/google/uuid"
)

// Los importes salen como texto con su escala fija ("49.00"): nunca como numero JSON, que
// el cliente leeria como float.
const dateLayout = "2006-01-02"

type limitDTO struct {
	Resource         string  `json:"resource"`
	Included         int64   `json:"included"`
	HardLimit        bool    `json:"hard_limit"`
	OverageUnitPrice *string `json:"overage_unit_price"`
}

type planDTO struct {
	ID            uuid.UUID  `json:"id"`
	Code          string     `json:"code"`
	Name          string     `json:"name"`
	Description   string     `json:"description"`
	Currency      string     `json:"currency"`
	BasePrice     string     `json:"base_price"`
	BillingPeriod string     `json:"billing_period"`
	Status        string     `json:"status"`
	Limits        []limitDTO `json:"limits"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

type subscriptionDTO struct {
	ID                 uuid.UUID  `json:"id"`
	TenantID           uuid.UUID  `json:"tenant_id"`
	PlanID             uuid.UUID  `json:"plan_id"`
	PlanCode           string     `json:"plan_code"`
	Plan               *planDTO   `json:"plan,omitempty"`
	Status             string     `json:"status"`
	CurrentPeriodStart string     `json:"current_period_start"`
	CurrentPeriodEnd   string     `json:"current_period_end"`
	TrialEndsAt        *time.Time `json:"trial_ends_at"`
	CancelAt           *time.Time `json:"cancel_at"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

type usageLineDTO struct {
	Resource  string  `json:"resource"`
	Kind      string  `json:"kind"`
	Used      int64   `json:"used"`
	Included  int64   `json:"included"`
	HardLimit bool    `json:"hard_limit"`
	Overage   int64   `json:"overage"`
	Percent   *string `json:"percent"`
}

type usageDTO struct {
	TenantID    uuid.UUID      `json:"tenant_id"`
	PlanCode    string         `json:"plan_code"`
	PeriodStart string         `json:"period_start"`
	PeriodEnd   string         `json:"period_end"`
	Resources   []usageLineDTO `json:"resources"`
}

type entitlementDTO struct {
	Allowed   bool   `json:"allowed"`
	Resource  string `json:"resource"`
	Limit     *int64 `json:"limit"`
	Used      int64  `json:"used"`
	Remaining *int64 `json:"remaining"`
	HardLimit bool   `json:"hard_limit"`
	Reason    string `json:"reason,omitempty"`
}

func toLimitDTOs(limits []domain.PlanLimit) []limitDTO {
	out := make([]limitDTO, 0, len(limits))
	for _, l := range limits {
		d := limitDTO{Resource: string(l.Resource), Included: l.Included, HardLimit: l.HardLimit}
		if l.OverageUnitPrice != nil {
			s := l.OverageUnitPrice.StringFixed(domain.UnitPriceScale)
			d.OverageUnitPrice = &s
		}
		out = append(out, d)
	}
	return out
}

func toPlanDTO(p *domain.Plan) planDTO {
	return planDTO{
		ID: p.ID, Code: p.Code, Name: p.Name, Description: p.Description, Currency: p.Currency,
		BasePrice: p.BasePrice.StringFixed(domain.PriceScale), BillingPeriod: string(p.BillingPeriod),
		Status: string(p.Status), Limits: toLimitDTOs(p.Limits), CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

func toPlanDTOs(plans []domain.Plan) []planDTO {
	out := make([]planDTO, 0, len(plans))
	for i := range plans {
		out = append(out, toPlanDTO(&plans[i]))
	}
	return out
}

// toSubscriptionDTO incluye el plan completo cuando se da; el listado de la plataforma
// lleva solo su codigo.
func toSubscriptionDTO(s *domain.Subscription, plan *domain.Plan) subscriptionDTO {
	d := subscriptionDTO{
		ID: s.ID, TenantID: s.TenantID, PlanID: s.PlanID, PlanCode: s.PlanCode, Status: string(s.Status),
		CurrentPeriodStart: s.CurrentPeriodStart.Format(dateLayout),
		CurrentPeriodEnd:   s.CurrentPeriodEnd.Format(dateLayout),
		TrialEndsAt:        s.TrialEndsAt, CancelAt: s.CancelAt, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt,
	}
	if plan != nil {
		p := toPlanDTO(plan)
		d.Plan = &p
	}
	return d
}

func toSubscriptionDTOs(subs []domain.Subscription) []subscriptionDTO {
	out := make([]subscriptionDTO, 0, len(subs))
	for i := range subs {
		out = append(out, toSubscriptionDTO(&subs[i], nil))
	}
	return out
}

func toUsageDTO(rep *domain.UsageReport) usageDTO {
	d := usageDTO{
		TenantID: rep.TenantID, PlanCode: rep.PlanCode,
		PeriodStart: rep.PeriodStart.Format(dateLayout), PeriodEnd: rep.PeriodEnd.Format(dateLayout),
		Resources: make([]usageLineDTO, 0, len(rep.Lines)),
	}
	for _, l := range rep.Lines {
		kind := "stock"
		if l.Flow {
			kind = "flow"
		}
		line := usageLineDTO{
			Resource: string(l.Resource), Kind: kind, Used: l.Used, Included: l.Included,
			HardLimit: l.HardLimit, Overage: l.Overage,
		}
		if l.Percent != nil {
			s := l.Percent.StringFixed(2)
			line.Percent = &s
		}
		d.Resources = append(d.Resources, line)
	}
	return d
}

func toEntitlementDTO(e domain.Entitlement) entitlementDTO {
	return entitlementDTO{
		Allowed: e.Allowed, Resource: string(e.Resource), Limit: e.Limit, Used: e.Used,
		Remaining: e.Remaining, HardLimit: e.HardLimit, Reason: string(e.Reason),
	}
}

// optionalTime distingue un campo ausente de uno enviado como null (que vacia la fecha).
type optionalTime struct {
	set   bool
	value *time.Time
}

func (o *optionalTime) UnmarshalJSON(b []byte) error {
	o.set = true
	if string(b) == "null" {
		o.value = nil
		return nil
	}
	var t time.Time
	if err := json.Unmarshal(b, &t); err != nil {
		return err
	}
	o.value = &t
	return nil
}

func (o optionalTime) toDomain() domain.OptionalTime {
	return domain.OptionalTime{Set: o.set, Value: o.value}
}
