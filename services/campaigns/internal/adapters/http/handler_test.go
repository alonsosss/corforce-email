package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/campaigns/internal/domain"
	"github.com/alonsosss/corforce-email/services/campaigns/internal/ports"
	"github.com/google/uuid"
)

func TestWriteErrorMapping(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{domain.ErrCampaignNotFound, 404, "NOT_FOUND"},
		{domain.TransitionError(domain.StatusCompleted, domain.StatusPaused), 409, "CONFLICT"},
		{domain.ErrNameTaken, 409, "CONFLICT"},
		{domain.ErrLockedWhilePaused, 409, "CONFLICT"},
		{domain.NewValidationError("name: es obligatorio"), 422, "VALIDATION_ERROR"},
		{domain.ErrTemplateVersionRequired, 422, "VALIDATION_ERROR"},
		{fmt.Errorf("%w (templates: archivada)", domain.ErrNoPublishedVersion), 422, "VALIDATION_ERROR"},
		{&ports.RateLimitedError{RetryAfter: 30 * time.Second}, 429, "RATE_LIMITED"},
		{&ports.BlockedError{Code: "SENDING_RESTRICTED", Message: "quejas"}, 403, "SENDING_RESTRICTED"},
		{&ports.RejectedError{Message: "dominio no verificado"}, 422, "SEND_REJECTED"},
		{&ports.RejectedError{Code: "TEMPLATE_NOT_MARKETING", Message: "la plantilla es transaccional"}, 422, "TEMPLATE_NOT_MARKETING"},
		{domain.ErrTemplateNotMarketing, 422, "VALIDATION_ERROR"},
		{fmt.Errorf("%w: templates", ports.ErrUnavailable), 503, "DEPENDENCY_UNAVAILABLE"},
		{errors.New("inesperado"), 500, "INTERNAL_ERROR"},
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		writeError(w, tc.err)
		var env struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &env)
		if w.Code != tc.status || env.Error.Code != tc.code {
			t.Errorf("%v: %d %s, se esperaba %d %s", tc.err, w.Code, env.Error.Code, tc.status, tc.code)
		}
	}
	w := httptest.NewRecorder()
	writeError(w, &ports.RateLimitedError{RetryAfter: 30 * time.Second})
	if w.Header().Get("Retry-After") != "30" {
		t.Fatalf("Retry-After: %q", w.Header().Get("Retry-After"))
	}
}

func TestDecodeOptional(t *testing.T) {
	var req startRequest
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	if err := decodeOptional(httptest.NewRecorder(), r, &req, defaultBodyLimit); err != nil || req.TemplateVersion != nil {
		t.Fatalf("cuerpo vacio: %v", err)
	}
	r = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"template_version":4}`))
	if err := decodeOptional(httptest.NewRecorder(), r, &req, defaultBodyLimit); err != nil || req.TemplateVersion == nil || *req.TemplateVersion != 4 {
		t.Fatalf("con version: %v", err)
	}
	r = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"otra":1}`))
	if err := decodeOptional(httptest.NewRecorder(), r, &req, defaultBodyLimit); err == nil {
		t.Fatal("campos desconocidos se rechazan")
	}
}

func TestCampaignResponseStatsOnlyWithPermission(t *testing.T) {
	version := 2
	c := &domain.Campaign{ID: uuid.New(), Name: "x", Status: domain.StatusSending, TemplateVersion: &version,
		Counters: domain.Counters{Sent: 10, Delivered: 8, Opened: 2}}
	plain, _ := json.Marshal(toResponse(c, false))
	if strings.Contains(string(plain), `"stats"`) {
		t.Fatalf("sin permiso no hay estadisticas: %s", plain)
	}
	full, _ := json.Marshal(toResponse(c, true))
	var out struct {
		Audience domain.Audience `json:"audience"`
		Stats    struct {
			Delivered int64        `json:"delivered"`
			Rates     domain.Rates `json:"rates"`
		} `json:"stats"`
	}
	if err := json.Unmarshal(full, &out); err != nil {
		t.Fatal(err)
	}
	if out.Stats.Delivered != 8 || out.Stats.Rates.DeliveryRate != "0.8000" || out.Stats.Rates.OpenRate != "0.2500" {
		t.Fatalf("estadisticas: %s", full)
	}
	if out.Audience.ListIDs == nil || !strings.Contains(string(full), `"list_ids":[]`) {
		t.Fatalf("la audiencia sale con listas vacias, no null: %s", full)
	}
}
