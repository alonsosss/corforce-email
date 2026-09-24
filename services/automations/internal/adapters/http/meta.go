package http

import (
	"net/http"

	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/services/automations/internal/app"
	"github.com/alonsosss/corforce-email/services/automations/internal/domain"
)

// statusMeta dice que acciones admite un flujo en cada estado; sale de los metodos del
// dominio sobre un flujo en ese estado, no de una tabla aparte.
type statusMeta struct {
	Status      domain.Status `json:"status"`
	Editable    bool          `json:"editable"`
	CanActivate bool          `json:"can_activate"`
	CanPause    bool          `json:"can_pause"`
	CanArchive  bool          `json:"can_archive"`
	Deletable   bool          `json:"deletable"`
}

type triggerMeta struct {
	Type domain.TriggerType `json:"type"`
	// CampaignFilter: el disparador admite trigger.campaign_id.
	CampaignFilter bool `json:"campaign_filter"`
	// Date: el disparador es un aniversario y exige attribute, hour y timezone.
	Date bool `json:"date"`
}

type conditionMeta struct {
	Kind domain.ConditionKind `json:"kind"`
	// Step: la condicion mira el correo de un paso send_email anterior (condition.step).
	Step bool `json:"step"`
}

type waitUnitMeta struct {
	Unit    string `json:"unit"`
	Seconds int64  `json:"seconds"`
}

type waitMeta struct {
	Units      []waitUnitMeta `json:"units"`
	MinSeconds int64          `json:"min_seconds"`
	MaxSeconds int64          `json:"max_seconds"`
}

type templateKindsMeta struct {
	SendEmail   string `json:"send_email"`
	DoubleOptIn string `json:"double_opt_in"`
}

type doiLimitsMeta struct {
	PerDay    int `json:"per_day"`
	Per30Days int `json:"per_30_days"`
}

type limitsMeta struct {
	MinSteps             int `json:"min_steps"`
	MaxSteps             int `json:"max_steps"`
	MaxNameLength        int `json:"max_name_length"`
	MaxDescriptionLength int `json:"max_description_length"`
	MaxPauseReasonLength int `json:"max_pause_reason_length"`
	MaxSearchLength      int `json:"max_search_length"`
	MaxDepth             int `json:"max_depth"`
	MaxStepIDLength      int `json:"max_step_id_length"`
	MaxTriggerHour       int `json:"max_trigger_hour"`
	MaxConditionValue    int `json:"max_condition_value_bytes"`
}

type paginationMeta struct {
	DefaultPageSize int `json:"default_page_size"`
	MaxPageSize     int `json:"max_page_size"`
}

type metaResponse struct {
	Statuses          []statusMeta       `json:"statuses"`
	TriggerTypes      []triggerMeta      `json:"trigger_types"`
	StepTypes         []domain.StepType  `json:"step_types"`
	ConditionKinds    []conditionMeta    `json:"condition_kinds"`
	Wait              waitMeta           `json:"wait"`
	RunStatuses       []domain.RunStatus `json:"run_statuses"`
	DOIStatuses       []domain.DOIStatus `json:"doi_statuses"`
	PauseReasonManual string             `json:"pause_reason_manual"`
	TemplateKinds     templateKindsMeta  `json:"template_kinds"`
	DOILimits         doiLimitsMeta      `json:"doi_limits"`
	Limits            limitsMeta         `json:"limits"`
	Pagination        paginationMeta     `json:"pagination"`
}

func buildMeta(limits domain.DOILimits) metaResponse {
	out := metaResponse{
		StepTypes:         domain.StepTypes(),
		RunStatuses:       domain.RunStatuses(),
		DOIStatuses:       domain.DOIStatuses(),
		PauseReasonManual: domain.PauseReasonManual,
		TemplateKinds:     templateKindsMeta{SendEmail: app.KindMarketing, DoubleOptIn: app.KindTransactional},
		DOILimits:         doiLimitsMeta{PerDay: limits.PerDay, Per30Days: limits.Per30Days},
		Wait: waitMeta{
			MinSeconds: int64(domain.MinWait.Seconds()),
			MaxSeconds: int64(domain.MaxWait.Seconds()),
		},
		Limits: limitsMeta{
			MinSteps: domain.MinSteps, MaxSteps: domain.MaxSteps, MaxNameLength: domain.MaxNameLen,
			MaxDescriptionLength: domain.MaxDescriptionLen, MaxPauseReasonLength: domain.MaxPauseReasonLen,
			MaxSearchLength: maxSearchLen, MaxDepth: domain.MaxDepth, MaxStepIDLength: domain.MaxStepIDLen,
			MaxTriggerHour: domain.MaxTriggerHour, MaxConditionValue: domain.MaxConditionValueBytes,
		},
		Pagination: paginationMeta{DefaultPageSize: defaultPerPage, MaxPageSize: maxPerPage},
	}
	for _, st := range domain.Statuses() {
		w := domain.Workflow{Status: st}
		out.Statuses = append(out.Statuses, statusMeta{
			Status: st, Editable: w.Editable(), CanActivate: w.CanActivate(), CanPause: w.CanPause(),
			CanArchive: w.CanArchive(), Deletable: w.Deletable(),
		})
	}
	for _, tt := range domain.TriggerTypes() {
		out.TriggerTypes = append(out.TriggerTypes, triggerMeta{Type: tt, CampaignFilter: tt.AcceptsCampaign(), Date: tt.IsDate()})
	}
	for _, k := range domain.ConditionKinds() {
		out.ConditionKinds = append(out.ConditionKinds, conditionMeta{Kind: k, Step: k.ReferencesStep()})
	}
	for _, u := range domain.WaitUnits() {
		out.Wait.Units = append(out.Wait.Units, waitUnitMeta{Unit: u.Code, Seconds: int64(u.Duration.Seconds())})
	}
	return out
}

// Meta publica los valores del dominio que la interfaz necesita para construir el editor
// de flujos y sus filtros: estados con sus acciones, disparadores, pasos, limites y el
// tope efectivo del doble opt-in. La interfaz no copia ninguno.
func (h *Handler) Meta(w http.ResponseWriter, r *http.Request) {
	response.JSON(w, http.StatusOK, buildMeta(h.uc.DOILimits()))
}
