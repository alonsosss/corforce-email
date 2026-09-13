package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/alonsosss/corforce-email/services/analytics/internal/domain"
)

// El catalogo sale de las constantes del dominio: si una cambia, la UI la recibe sin
// copiarla.
func TestMetaPublicaLasConstantesDelDominio(t *testing.T) {
	rec := httptest.NewRecorder()
	(&Handler{}).Meta(rec, httptest.NewRequest(http.MethodGet, "/meta", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var body struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"classes", "timezone", "range", "domains", "pagination"} {
		if _, ok := body.Data[k]; !ok {
			t.Errorf("falta la clave %q", k)
		}
	}
	var classes []string
	_ = json.Unmarshal(body.Data["classes"], &classes)
	want := []string{}
	for _, c := range domain.Classes() {
		want = append(want, string(c))
	}
	if !slices.Equal(classes, want) {
		t.Errorf("classes = %v, want %v", classes, want)
	}
	m := buildMeta()
	if m.Timezone != domain.ReportTimezone || m.Range.MaxDays != domain.MaxRangeDays ||
		m.Range.DefaultDays != domain.DefaultRangeDays || m.Domains.MaxLimit != domain.MaxDomainLimit ||
		m.Domains.DefaultLimit != domain.DefaultDomainLimit || m.Pagination.MaxPerPage != maxPerPage {
		t.Errorf("catalogo desalineado con el dominio: %+v", m)
	}
}
