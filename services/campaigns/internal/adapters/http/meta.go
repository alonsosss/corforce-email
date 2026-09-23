package http

import (
	"net/http"

	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/services/campaigns/internal/domain"
)

// statusMeta dice que acciones admite una campana en cada estado; sale de los metodos del
// dominio sobre una campana en ese estado. El servicio las vuelve a comprobar (409).
type statusMeta struct {
	Status        domain.Status `json:"status"`
	Editable      bool          `json:"editable"`
	ContentLocked bool          `json:"content_locked"`
	CanSchedule   bool          `json:"can_schedule"`
	CanStart      bool          `json:"can_start"`
	CanPause      bool          `json:"can_pause"`
	CanResume     bool          `json:"can_resume"`
	CanCancel     bool          `json:"can_cancel"`
	Deletable     bool          `json:"deletable"`
}

type limitsMeta struct {
	MaxNameLength        int `json:"max_name_length"`
	MaxDescriptionLength int `json:"max_description_length"`
	MaxDisplayNameLength int `json:"max_display_name_length"`
	MaxEmailLength       int `json:"max_email_length"`
	MaxAudienceIDs       int `json:"max_audience_ids"`
	MaxSearchLength      int `json:"max_search_length"`
}

type scheduleMeta struct {
	MinLeadSeconds    int64 `json:"min_lead_seconds"`
	MaxHorizonSeconds int64 `json:"max_horizon_seconds"`
}

type abTestMeta struct {
	Criteria                 []domain.ABCriterion    `json:"criteria"`
	MinVariants              int                     `json:"min_variants"`
	MaxVariants              int                     `json:"max_variants"`
	MinSamplePercent         int                     `json:"min_sample_percent"`
	MaxSamplePercent         int                     `json:"max_sample_percent"`
	MinDecisionWindowMinutes int                     `json:"min_decision_window_minutes"`
	MaxDecisionWindowMinutes int                     `json:"max_decision_window_minutes"`
	DecisionReasons          []domain.DecisionReason `json:"decision_reasons"`
}

type resendMeta struct {
	MinDelayMinutes int `json:"min_delay_minutes"`
	MaxDelayMinutes int `json:"max_delay_minutes"`
}

type phasesMeta struct {
	Kinds    []domain.PhaseKind   `json:"kinds"`
	Statuses []domain.PhaseStatus `json:"statuses"`
}

type paginationMeta struct {
	DefaultPageSize int `json:"default_page_size"`
	MaxPageSize     int `json:"max_page_size"`
}

type metaResponse struct {
	Statuses          []statusMeta         `json:"statuses"`
	BatchStatuses     []domain.BatchStatus `json:"batch_statuses"`
	PauseReasonManual string               `json:"pause_reason_manual"`
	MaxTestRecipients int                  `json:"max_test_recipients"`
	// BatchSize es el tamano efectivo de lote; MaxBatchSize, el tope del lote de
	// transactional.
	BatchSize    int            `json:"batch_size"`
	MaxBatchSize int            `json:"max_batch_size"`
	Limits       limitsMeta     `json:"limits"`
	Schedule     scheduleMeta   `json:"schedule"`
	Pagination   paginationMeta `json:"pagination"`
	ABTest       abTestMeta     `json:"ab_test"`
	Resend       resendMeta     `json:"resend"`
	Phases       phasesMeta     `json:"phases"`
	// MaxSubjectLength acota el asunto de una variante y el del reenvio.
	MaxSubjectLength int `json:"max_subject_length"`
}

func buildMeta(batchSize int) metaResponse {
	out := metaResponse{
		BatchStatuses:     domain.BatchStatuses(),
		PauseReasonManual: domain.PauseReasonManual,
		MaxTestRecipients: domain.MaxTestRecipients,
		BatchSize:         batchSize,
		MaxBatchSize:      domain.MaxBatchSize,
		Limits: limitsMeta{
			MaxNameLength: domain.MaxNameLength, MaxDescriptionLength: domain.MaxDescriptionLength,
			MaxDisplayNameLength: domain.MaxDisplayNameLength, MaxEmailLength: domain.MaxEmailLength,
			MaxAudienceIDs: domain.MaxAudienceIDs, MaxSearchLength: maxSearchLength,
		},
		Schedule: scheduleMeta{
			MinLeadSeconds:    int64(domain.MinScheduleLead.Seconds()),
			MaxHorizonSeconds: int64(domain.MaxScheduleHorizon.Seconds()),
		},
		Pagination: paginationMeta{DefaultPageSize: defaultPerPage, MaxPageSize: maxPerPage},
		ABTest: abTestMeta{
			Criteria:    domain.ABCriteria(),
			MinVariants: domain.MinABVariants, MaxVariants: domain.MaxABVariants,
			MinSamplePercent: domain.MinSamplePercent, MaxSamplePercent: domain.MaxSamplePercent,
			MinDecisionWindowMinutes: int(domain.MinDecisionWindow.Minutes()),
			MaxDecisionWindowMinutes: int(domain.MaxDecisionWindow.Minutes()),
			DecisionReasons:          domain.DecisionReasons(),
		},
		Resend: resendMeta{
			MinDelayMinutes: int(domain.MinResendDelay.Minutes()),
			MaxDelayMinutes: int(domain.MaxResendDelay.Minutes()),
		},
		Phases:           phasesMeta{Kinds: domain.PhaseKinds(), Statuses: domain.PhaseStatuses()},
		MaxSubjectLength: domain.MaxSubjectLength,
	}
	for _, st := range domain.Statuses() {
		c := domain.Campaign{Status: st}
		out.Statuses = append(out.Statuses, statusMeta{
			Status: st, Editable: c.Editable(), ContentLocked: c.ContentLocked(), CanSchedule: c.CanSchedule(),
			CanStart: c.CanStart(), CanPause: c.CanPause(), CanResume: c.CanResume(), CanCancel: c.CanCancel(),
			Deletable: c.Deletable(),
		})
	}
	return out
}

// Meta publica los estados con sus acciones, los topes y el tamano de lote que la
// interfaz necesita; ninguno se copia en el cliente.
func (h *Handler) Meta(w http.ResponseWriter, r *http.Request) {
	response.JSON(w, http.StatusOK, buildMeta(h.uc.BatchSize()))
}
