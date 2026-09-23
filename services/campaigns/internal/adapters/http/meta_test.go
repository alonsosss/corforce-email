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
	"github.com/alonsosss/corforce-email/services/campaigns/internal/app"
	"github.com/alonsosss/corforce-email/services/campaigns/internal/domain"
	"github.com/google/uuid"
)

// access-control inalcanzable: solo un rol del sistema pasa sin consultarlo.
const unreachableAccessControl = "http://127.0.0.1:9"

// metaContract escribe a mano los nombres JSON que consume la interfaz.
type metaContract struct {
	Statuses []struct {
		Status        string `json:"status"`
		Editable      bool   `json:"editable"`
		ContentLocked bool   `json:"content_locked"`
		CanSchedule   bool   `json:"can_schedule"`
		CanStart      bool   `json:"can_start"`
		CanPause      bool   `json:"can_pause"`
		CanResume     bool   `json:"can_resume"`
		CanCancel     bool   `json:"can_cancel"`
		Deletable     bool   `json:"deletable"`
	} `json:"statuses"`
	BatchStatuses     []string `json:"batch_statuses"`
	PauseReasonManual string   `json:"pause_reason_manual"`
	MaxTestRecipients int      `json:"max_test_recipients"`
	BatchSize         int      `json:"batch_size"`
	MaxBatchSize      int      `json:"max_batch_size"`
	Limits            struct {
		MaxNameLength        int `json:"max_name_length"`
		MaxDescriptionLength int `json:"max_description_length"`
		MaxDisplayNameLength int `json:"max_display_name_length"`
		MaxEmailLength       int `json:"max_email_length"`
		MaxAudienceIDs       int `json:"max_audience_ids"`
		MaxSearchLength      int `json:"max_search_length"`
	} `json:"limits"`
	Schedule struct {
		MinLeadSeconds    int64 `json:"min_lead_seconds"`
		MaxHorizonSeconds int64 `json:"max_horizon_seconds"`
	} `json:"schedule"`
	Pagination struct {
		DefaultPageSize int `json:"default_page_size"`
		MaxPageSize     int `json:"max_page_size"`
	} `json:"pagination"`
	ABTest struct {
		Criteria                 []string `json:"criteria"`
		MinVariants              int      `json:"min_variants"`
		MaxVariants              int      `json:"max_variants"`
		MinSamplePercent         int      `json:"min_sample_percent"`
		MaxSamplePercent         int      `json:"max_sample_percent"`
		MinDecisionWindowMinutes int      `json:"min_decision_window_minutes"`
		MaxDecisionWindowMinutes int      `json:"max_decision_window_minutes"`
		DecisionReasons          []string `json:"decision_reasons"`
	} `json:"ab_test"`
	Resend struct {
		MinDelayMinutes int `json:"min_delay_minutes"`
		MaxDelayMinutes int `json:"max_delay_minutes"`
	} `json:"resend"`
	Phases struct {
		Kinds    []string `json:"kinds"`
		Statuses []string `json:"statuses"`
	} `json:"phases"`
	MaxSubjectLength int `json:"max_subject_length"`
}

func names[T ~string](xs []T) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = string(x)
	}
	return out
}

func metaRequest(roles ...string) *httptest.ResponseRecorder {
	h := NewHandler(app.New(app.Deps{Config: app.Config{BatchSize: 250}}), authz.NewChecker(unreachableAccessControl, ""))
	req := httptest.NewRequest(http.MethodGet, "/meta", nil)
	ctx := middleware.WithTenantID(req.Context(), uuid.NewString())
	ctx = context.WithValue(ctx, middleware.CtxUserID, uuid.NewString())
	ctx = context.WithValue(ctx, middleware.CtxRoles, roles)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req.WithContext(ctx))
	return rec
}

func TestMetaPublicaEstadosAccionesYTopes(t *testing.T) {
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
	statuses := make([]string, 0, len(got.Statuses))
	for _, s := range got.Statuses {
		statuses = append(statuses, s.Status)
		c := domain.Campaign{Status: domain.Status(s.Status)}
		want := [8]bool{c.Editable(), c.ContentLocked(), c.CanSchedule(), c.CanStart(), c.CanPause(), c.CanResume(), c.CanCancel(), c.Deletable()}
		have := [8]bool{s.Editable, s.ContentLocked, s.CanSchedule, s.CanStart, s.CanPause, s.CanResume, s.CanCancel, s.Deletable}
		if want != have {
			t.Errorf("acciones de %s: %v, dominio %v", s.Status, have, want)
		}
	}
	domainStatuses := make([]string, 0, len(domain.Statuses()))
	for _, s := range domain.Statuses() {
		domainStatuses = append(domainStatuses, string(s))
	}
	batchStatuses := make([]string, 0, len(domain.BatchStatuses()))
	for _, s := range domain.BatchStatuses() {
		batchStatuses = append(batchStatuses, string(s))
	}
	checks := []struct {
		name      string
		got, want any
	}{
		{"statuses", statuses, domainStatuses},
		{"batch_statuses", got.BatchStatuses, batchStatuses},
		{"pause_reason_manual", got.PauseReasonManual, domain.PauseReasonManual},
		{"max_test_recipients", got.MaxTestRecipients, domain.MaxTestRecipients},
		{"batch_size", got.BatchSize, 250},
		{"max_batch_size", got.MaxBatchSize, domain.MaxBatchSize},
		{"limits", [5]int{got.Limits.MaxNameLength, got.Limits.MaxDescriptionLength, got.Limits.MaxDisplayNameLength, got.Limits.MaxEmailLength, got.Limits.MaxAudienceIDs},
			[5]int{domain.MaxNameLength, domain.MaxDescriptionLength, domain.MaxDisplayNameLength, domain.MaxEmailLength, domain.MaxAudienceIDs}},
		{"max_search_length", got.Limits.MaxSearchLength, maxSearchLength},
		{"schedule", [2]int64{got.Schedule.MinLeadSeconds, got.Schedule.MaxHorizonSeconds},
			[2]int64{int64(domain.MinScheduleLead.Seconds()), int64(domain.MaxScheduleHorizon.Seconds())}},
		{"pagination", [2]int{got.Pagination.DefaultPageSize, got.Pagination.MaxPageSize}, [2]int{defaultPerPage, maxPerPage}},
		{"ab_test.criteria", got.ABTest.Criteria, []string{"opens", "clicks"}},
		{"ab_test.limits", [6]int{got.ABTest.MinVariants, got.ABTest.MaxVariants, got.ABTest.MinSamplePercent,
			got.ABTest.MaxSamplePercent, got.ABTest.MinDecisionWindowMinutes, got.ABTest.MaxDecisionWindowMinutes},
			[6]int{2, 4, 10, 50, 60, 72 * 60}},
		{"ab_test.decision_reasons", got.ABTest.DecisionReasons, names(domain.DecisionReasons())},
		{"resend", [2]int{got.Resend.MinDelayMinutes, got.Resend.MaxDelayMinutes}, [2]int{24 * 60, 7 * 24 * 60}},
		{"phases.kinds", got.Phases.Kinds, []string{"main", "sample", "winner", "zone", "resend"}},
		{"phases.statuses", got.Phases.Statuses, []string{"pending", "done"}},
		{"max_subject_length", got.MaxSubjectLength, domain.MaxSubjectLength},
	}
	for _, c := range checks {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s = %v, quiero %v", c.name, c.got, c.want)
		}
	}
}

// El tamano de lote publicado es el efectivo: un valor fuera de rango se acota al tope.
func TestMetaPublicaElLoteEfectivo(t *testing.T) {
	uc := app.New(app.Deps{Config: app.Config{BatchSize: domain.MaxBatchSize + 1}})
	if got := buildMeta(uc.BatchSize()).BatchSize; got != domain.MaxBatchSize {
		t.Fatalf("lote publicado %d, tope %d", got, domain.MaxBatchSize)
	}
}

func TestMetaExigeElPermisoDeLectura(t *testing.T) {
	if rec := metaRequest("editor"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("sin politica comprobable debe fallar cerrado: status %d", rec.Code)
	}
}
