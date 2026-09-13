package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/analytics/internal/app"
	"github.com/alonsosss/corforce-email/services/analytics/internal/domain"
	"github.com/google/uuid"
)

var fixedNow = time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)

type allowAll struct{}

func (allowAll) RequirePermission(string, string, string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler { return next }
}

type fakeReports struct {
	totals   domain.Counters
	series   []domain.DayCounters
	query    domain.ClassQuery
	limit    int
	campaign *domain.CampaignSummary
}

func (f *fakeReports) Totals(_ context.Context, _ uuid.UUID, q domain.ClassQuery) (domain.Counters, error) {
	f.query = q
	return f.totals, nil
}

func (f *fakeReports) Series(_ context.Context, _ uuid.UUID, q domain.ClassQuery) ([]domain.DayCounters, error) {
	f.query = q
	return f.series, nil
}

func (f *fakeReports) CountCampaigns(context.Context, uuid.UUID) (int64, error) { return 0, nil }

func (f *fakeReports) ListCampaigns(context.Context, uuid.UUID, int, int) ([]domain.CampaignSummary, error) {
	return nil, nil
}

func (f *fakeReports) GetCampaign(_ context.Context, _, id uuid.UUID) (*domain.CampaignSummary, error) {
	if f.campaign == nil || f.campaign.CampaignID != id {
		return nil, domain.ErrCampaignNotFound
	}
	return f.campaign, nil
}

func (f *fakeReports) CampaignSeries(context.Context, uuid.UUID, uuid.UUID, domain.Range) ([]domain.DayCounters, error) {
	return f.series, nil
}

func (f *fakeReports) TopDomains(_ context.Context, _ uuid.UUID, q domain.ClassQuery, limit int) ([]domain.DomainStats, error) {
	f.query, f.limit = q, limit
	return []domain.DomainStats{{RecipientDomain: "example.com", Totals: domain.Counters{Sent: 4, BouncedHard: 1}}}, nil
}

func serve(t *testing.T, rep *fakeReports, path string, withTenant bool) *httptest.ResponseRecorder {
	t.Helper()
	uc := app.New(app.Deps{Reports: rep, Now: func() time.Time { return fixedNow }})
	h := middleware.InjectFromGateway(NewHandler(uc, allowAll{}).Routes())
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if withTenant {
		req.Header.Set("X-Tenant-ID", uuid.NewString())
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder, dst any) {
	t.Helper()
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("cuerpo no es JSON: %v (%s)", err, rec.Body.String())
	}
	if err := json.Unmarshal(env.Data, dst); err != nil {
		t.Fatalf("data: %v", err)
	}
}

func TestOverviewPorDefectoConTasasEnCero(t *testing.T) {
	rec := serve(t, &fakeReports{}, "/overview", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got overviewResponse
	decode(t, rec, &got)
	if got.From != "2026-08-15" || got.To != "2026-09-13" || got.Class != nil || got.Timezone != "UTC" {
		t.Fatalf("rango por defecto: %+v", got)
	}
	if got.Rates.Delivery != "0.0000" || got.Rates.Complaint != "0.0000" {
		t.Fatalf("tasas con denominador cero: %+v", got.Rates)
	}
}

func TestOverviewConClaseYTotales(t *testing.T) {
	rep := &fakeReports{totals: domain.Counters{Sent: 4, Delivered: 2, OpenedUnique: 1}}
	rec := serve(t, rep, "/overview?from=2026-09-01&to=2026-09-10&class=marketing", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got overviewResponse
	decode(t, rec, &got)
	if got.Class == nil || *got.Class != "marketing" || rep.query.Class != domain.ClassMarketing || rep.query.Range.Days() != 10 {
		t.Fatalf("filtro: %+v / %+v", got, rep.query)
	}
	if got.Rates.Delivery != "0.5000" || got.Rates.Open != "0.5000" || got.Totals.Sent != 4 {
		t.Fatalf("totales y tasas: %+v", got)
	}
}

func TestValidacionDeParametros(t *testing.T) {
	cases := []string{
		"/overview?from=2026-09-10&to=2026-09-01",
		"/overview?from=2025-01-01&to=2026-01-02",
		"/timeseries?from=01-09-2026",
		"/timeseries?class=corporate",
		"/domains?limit=0",
		"/domains?limit=101",
		"/domains?limit=veinte",
	}
	for _, path := range cases {
		if rec := serve(t, &fakeReports{}, path, true); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status %d, se esperaba 422 (%s)", path, rec.Code, rec.Body.String())
		}
	}
}

func TestSinEmpresaNoHayDatos(t *testing.T) {
	if rec := serve(t, &fakeReports{}, "/overview", false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestSerieDiaria(t *testing.T) {
	rep := &fakeReports{series: []domain.DayCounters{
		{Day: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)},
		{Day: time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC), Counters: domain.Counters{Sent: 3}},
	}}
	rec := serve(t, rep, "/timeseries?from=2026-09-01&to=2026-09-02", true)
	var got struct {
		Points []map[string]any `json:"points"`
	}
	decode(t, rec, &got)
	if len(got.Points) != 2 || got.Points[0]["day"] != "2026-09-01" || got.Points[1]["sent"] != float64(3) || got.Points[0]["opened_unique"] != float64(0) {
		t.Fatalf("puntos: %+v", got.Points)
	}
}

func TestDominios(t *testing.T) {
	rep := &fakeReports{}
	rec := serve(t, rep, "/domains?limit=5&class=transactional", true)
	if rec.Code != http.StatusOK || rep.limit != 5 {
		t.Fatalf("status %d, limit %d", rec.Code, rep.limit)
	}
	var got domainsResponse
	decode(t, rec, &got)
	if len(got.Domains) != 1 || got.Domains[0].Rates.Bounce != "0.2500" || got.Limit != 5 {
		t.Fatalf("dominios: %+v", got)
	}
}

func TestCampana(t *testing.T) {
	id := uuid.New()
	status := "completed"
	first := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	rep := &fakeReports{campaign: &domain.CampaignSummary{CampaignID: id, Status: &status, FirstDay: &first, LastDay: &first, Totals: domain.Counters{Sent: 2, Delivered: 2}}}

	if rec := serve(t, rep, "/campaigns/no-es-uuid", true); rec.Code != http.StatusBadRequest {
		t.Fatalf("id invalido: %d", rec.Code)
	}
	if rec := serve(t, rep, "/campaigns/"+uuid.NewString(), true); rec.Code != http.StatusNotFound {
		t.Fatalf("desconocida: %d", rec.Code)
	}
	rec := serve(t, rep, "/campaigns/"+id.String(), true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got campaignDetailResponse
	decode(t, rec, &got)
	if got.CampaignID != id || got.From != "2026-09-01" || got.To != "2026-09-01" || got.Rates.Delivery != "1.0000" || got.FirstDay == nil {
		t.Fatalf("detalle: %+v", got)
	}
}
