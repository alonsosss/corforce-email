package http

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/campaigns/internal/domain"
	"github.com/google/uuid"
)

func TestOptionalFieldTriState(t *testing.T) {
	conv := (*resendRequest).toDomain
	absent, err := optionalField(nil, conv)
	if err != nil || absent.Set {
		t.Fatalf("ausente no toca: %+v %v", absent, err)
	}
	null, err := optionalField(json.RawMessage(" null "), conv)
	if err != nil || !null.Set || null.Value != nil {
		t.Fatalf("null quita: %+v %v", null, err)
	}
	set, err := optionalField(json.RawMessage(`{"subject":"Otra vez","delay_minutes":1440}`), conv)
	if err != nil || !set.Set || set.Value.Subject != "Otra vez" || set.Value.DelayMinutes != 1440 {
		t.Fatalf("objeto sustituye: %+v %v", set, err)
	}
	if _, err := optionalField(json.RawMessage(`{"subject":"x","delay":1}`), conv); err == nil {
		t.Fatal("un campo fuera del contrato se rechaza")
	}
	if _, err := optionalField(json.RawMessage(`{"variants":[{"subject":"a","pinned_version":3}]}`), (*abTestRequest).toDomain); err == nil {
		t.Fatal("la version fijada no la elige el cliente")
	}
	if err := errOptional("ab_test", json.Unmarshal([]byte("{"), &struct{}{})); !strings.HasPrefix(err.Error(), "ab_test: ") {
		t.Fatalf("el error nombra el campo: %v", err)
	}
}

func TestWriteErrorOptions(t *testing.T) {
	for err, status := range map[error]int{domain.ErrABWithTimezone: 422, domain.ErrABAlreadyDecided: 409} {
		w := httptest.NewRecorder()
		writeError(w, err)
		if w.Code != status {
			t.Errorf("%v: %d, se esperaba %d", err, w.Code, status)
		}
	}
}

func TestCampaignResponseOptions(t *testing.T) {
	version, winner, pinned := 2, 1, 5
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	local, _ := domain.ParseLocalDateTime("2026-10-01T09:00")
	c := &domain.Campaign{ID: uuid.New(), Status: domain.StatusSending, TemplateVersion: &version,
		ABTest: &domain.ABTest{Criterion: domain.CriterionClicks, SamplePercent: 20, DecisionWindowMinutes: 60,
			Variants: []domain.ABVariant{{Subject: "A"}, {Subject: "B", PinnedVersion: &pinned}}},
		ABWinner: &winner, ABDecidedAt: &now,
		ABDecision: &domain.ABDecision{Winner: 1, Criterion: domain.CriterionClicks, Reason: domain.ReasonCriterion,
			Results: []domain.VariantResult{{Variant: 0, Delivered: 10, Clicked: 1}, {Variant: 1, Delivered: 10, Clicked: 3}}},
		Resend:           &domain.Resend{Subject: "Otra vez", DelayMinutes: 1440},
		TimezoneDelivery: &domain.TimezoneDelivery{LocalSendAt: local, FallbackTimezone: "America/Lima"},
	}
	var out struct {
		ABTest struct {
			Variants []struct {
				Label         string `json:"label"`
				PinnedVersion *int   `json:"pinned_version"`
			} `json:"variants"`
		} `json:"ab_test"`
		ABWinner   *int `json:"ab_winner"`
		ABDecision *struct {
			Reason  string `json:"reason"`
			Results []struct {
				Label     string `json:"label"`
				ClickRate string `json:"click_rate"`
			} `json:"results"`
		} `json:"ab_decision"`
		Resend           *resendResponse           `json:"resend"`
		TimezoneDelivery *timezoneDeliveryResponse `json:"timezone_delivery"`
	}
	full, _ := json.Marshal(toResponse(c, true))
	if err := json.Unmarshal(full, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.ABTest.Variants) != 2 || out.ABTest.Variants[1].Label != "B" || *out.ABTest.Variants[1].PinnedVersion != 5 ||
		*out.ABWinner != 1 || out.ABDecision == nil || out.ABDecision.Results[1].ClickRate != "0.3000" {
		t.Fatalf("respuesta con opciones: %s", full)
	}
	if out.Resend.Subject != "Otra vez" || out.TimezoneDelivery.LocalSendAt != "2026-10-01T09:00" || out.TimezoneDelivery.FallbackTimezone != "America/Lima" {
		t.Fatalf("reenvio y zona: %s", full)
	}
	plain, _ := json.Marshal(toResponse(c, false))
	if strings.Contains(string(plain), "ab_decision") || !strings.Contains(string(plain), `"ab_winner":1`) {
		t.Fatalf("la foto de la decision es una estadistica; la ganadora no: %s", plain)
	}
	empty, _ := json.Marshal(toResponse(&domain.Campaign{ID: uuid.New()}, false))
	for _, field := range []string{`"ab_test":null`, `"resend":null`, `"timezone_delivery":null`, `"ab_winner":null`} {
		if !strings.Contains(string(empty), field) {
			t.Fatalf("sin opciones sale %s: %s", field, empty)
		}
	}
}
