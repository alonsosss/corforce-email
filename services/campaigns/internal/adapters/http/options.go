package http

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/services/campaigns/internal/domain"
	"github.com/google/uuid"
)

// Prueba A/B, reenvio a quien no abrio y envio por zona horaria: su forma en la API.

type variantRequest struct {
	Subject         string     `json:"subject"`
	TemplateID      *uuid.UUID `json:"template_id"`
	TemplateVersion *int       `json:"template_version"`
}

type abTestRequest struct {
	Criterion             domain.ABCriterion `json:"criterion"`
	SamplePercent         int                `json:"sample_percent"`
	DecisionWindowMinutes int                `json:"decision_window_minutes"`
	Variants              []variantRequest   `json:"variants"`
}

func (a *abTestRequest) toDomain() *domain.ABTest {
	if a == nil {
		return nil
	}
	out := &domain.ABTest{
		Criterion: a.Criterion, SamplePercent: a.SamplePercent, DecisionWindowMinutes: a.DecisionWindowMinutes,
		Variants: make([]domain.ABVariant, len(a.Variants)),
	}
	for i, v := range a.Variants {
		out.Variants[i] = domain.ABVariant{Subject: v.Subject, TemplateID: v.TemplateID, TemplateVersion: v.TemplateVersion}
	}
	return out
}

type resendRequest struct {
	Subject      string `json:"subject"`
	DelayMinutes int    `json:"delay_minutes"`
}

func (r *resendRequest) toDomain() *domain.Resend {
	if r == nil {
		return nil
	}
	return &domain.Resend{Subject: r.Subject, DelayMinutes: r.DelayMinutes}
}

// optionalField lee un campo de PATCH que admite null para quitarlo: ausente no lo
// toca, null lo quita y un objeto lo sustituye.
func optionalField[T any, D any](raw json.RawMessage, conv func(*T) *D) (domain.Optional[D], error) {
	if len(raw) == 0 {
		return domain.Optional[D]{}, nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return domain.Optional[D]{Set: true}, nil
	}
	var v T
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return domain.Optional[D]{}, err
	}
	return domain.Optional[D]{Set: true, Value: conv(&v)}, nil
}

type variantResponse struct {
	Label           string     `json:"label"`
	Subject         string     `json:"subject"`
	TemplateID      *uuid.UUID `json:"template_id"`
	TemplateVersion *int       `json:"template_version"`
	PinnedVersion   *int       `json:"pinned_version"`
}

type abTestResponse struct {
	Criterion             domain.ABCriterion `json:"criterion"`
	SamplePercent         int                `json:"sample_percent"`
	DecisionWindowMinutes int                `json:"decision_window_minutes"`
	Variants              []variantResponse  `json:"variants"`
}

func abTestOf(a *domain.ABTest) *abTestResponse {
	if a == nil {
		return nil
	}
	out := &abTestResponse{
		Criterion: a.Criterion, SamplePercent: a.SamplePercent, DecisionWindowMinutes: a.DecisionWindowMinutes,
		Variants: make([]variantResponse, len(a.Variants)),
	}
	for i, v := range a.Variants {
		out.Variants[i] = variantResponse{Label: domain.VariantLabel(i), Subject: v.Subject, TemplateID: v.TemplateID,
			TemplateVersion: v.TemplateVersion, PinnedVersion: v.PinnedVersion}
	}
	return out
}

// engagementResponse son los contadores unicos de una fase o variante con sus tasas
// sobre entregados.
type engagementResponse struct {
	Accepted  int64  `json:"accepted"`
	Delivered int64  `json:"delivered"`
	Opened    int64  `json:"opened"`
	Clicked   int64  `json:"clicked"`
	OpenRate  string `json:"open_rate"`
	ClickRate string `json:"click_rate"`
}

func engagementOf(accepted, delivered, opened, clicked int64) engagementResponse {
	return engagementResponse{Accepted: accepted, Delivered: delivered, Opened: opened, Clicked: clicked,
		OpenRate: domain.Ratio(opened, delivered), ClickRate: domain.Ratio(clicked, delivered)}
}

type decisionResultResponse struct {
	Variant int    `json:"variant"`
	Label   string `json:"label"`
	engagementResponse
}

type decisionResponse struct {
	Winner    int                      `json:"winner"`
	Criterion domain.ABCriterion       `json:"criterion"`
	Reason    domain.DecisionReason    `json:"reason"`
	Results   []decisionResultResponse `json:"results"`
}

func decisionOf(d *domain.ABDecision) *decisionResponse {
	if d == nil {
		return nil
	}
	out := &decisionResponse{Winner: d.Winner, Criterion: d.Criterion, Reason: d.Reason,
		Results: make([]decisionResultResponse, len(d.Results))}
	for i, r := range d.Results {
		out.Results[i] = decisionResultResponse{Variant: r.Variant, Label: domain.VariantLabel(r.Variant),
			engagementResponse: engagementOf(r.Accepted, r.Delivered, r.Opened, r.Clicked)}
	}
	return out
}

type resendResponse struct {
	Subject      string `json:"subject"`
	DelayMinutes int    `json:"delay_minutes"`
}

func resendOf(r *domain.Resend) *resendResponse {
	if r == nil {
		return nil
	}
	return &resendResponse{Subject: r.Subject, DelayMinutes: r.DelayMinutes}
}

type timezoneDeliveryResponse struct {
	LocalSendAt      string `json:"local_send_at"`
	FallbackTimezone string `json:"fallback_timezone"`
}

func timezoneDeliveryOf(t *domain.TimezoneDelivery) *timezoneDeliveryResponse {
	if t == nil {
		return nil
	}
	return &timezoneDeliveryResponse{LocalSendAt: t.LocalSendAt.String(), FallbackTimezone: t.FallbackTimezone}
}

// ── Plan de envio ────────────────────────────────────────────────────────────

type phaseResponse struct {
	ID          uuid.UUID          `json:"id"`
	Kind        domain.PhaseKind   `json:"kind"`
	Variant     *int               `json:"variant"`
	SlotAt      *time.Time         `json:"slot_at"`
	NotBefore   *time.Time         `json:"not_before"`
	Status      domain.PhaseStatus `json:"status"`
	Targeted    int                `json:"targeted"`
	Accepted    int                `json:"accepted"`
	Suppressed  int                `json:"suppressed"`
	StartedAt   *time.Time         `json:"started_at"`
	CompletedAt *time.Time         `json:"completed_at"`
}

type phaseEngagementResponse struct {
	Kind    domain.PhaseKind `json:"kind"`
	Variant *int             `json:"variant"`
	engagementResponse
}

type planResponse struct {
	Phases     []phaseResponse           `json:"phases"`
	Engagement []phaseEngagementResponse `json:"engagement"`
}

// Plan devuelve las fases de envio de la campana (muestras, ganadora, tramos de zona,
// reenvio) con sus totales y la interaccion unica por fase y variante.
func (h *Handler) Plan(w http.ResponseWriter, r *http.Request) {
	tenantID, id, ok := target(w, r)
	if !ok {
		return
	}
	_, plan, err := h.uc.Plan(r.Context(), tenantID, id)
	if err != nil {
		writeError(w, err)
		return
	}
	out := planResponse{
		Phases:     make([]phaseResponse, len(plan.Phases)),
		Engagement: make([]phaseEngagementResponse, len(plan.Engagement)),
	}
	for i, p := range plan.Phases {
		out.Phases[i] = phaseResponse{ID: p.ID, Kind: p.Kind, Variant: p.Variant, SlotAt: p.SlotAt, NotBefore: p.NotBefore,
			Status: p.Status, Targeted: p.Targeted, Accepted: p.Accepted, Suppressed: p.Suppressed,
			StartedAt: p.StartedAt, CompletedAt: p.CompletedAt}
	}
	for i, e := range plan.Engagement {
		out.Engagement[i] = phaseEngagementResponse{Kind: e.Kind, Variant: e.Variant,
			engagementResponse: engagementOf(e.Accepted, e.Delivered, e.Opened, e.Clicked)}
	}
	response.JSON(w, http.StatusOK, out)
}

// errOptional explica un campo opcional ilegible sin exponer detalles del decodificador.
func errOptional(field string, err error) error {
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) {
		return errors.New(field + ": JSON no valido")
	}
	return errors.New(field + ": " + err.Error())
}
