package http

import (
	"bytes"
	"encoding/json"
	nethttp "net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/automations/internal/app"
	"github.com/alonsosss/corforce-email/services/automations/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/automations/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// recordingGuard deja pasar y anota el permiso que exigio cada peticion.
type recordingGuard struct{ seen [][3]string }

func (g *recordingGuard) RequirePermission(module, resource, action string) func(nethttp.Handler) nethttp.Handler {
	return func(next nethttp.Handler) nethttp.Handler {
		return nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
			g.seen = append(g.seen, [3]string{module, resource, action})
			next.ServeHTTP(w, r)
		})
	}
}

// metaContract escribe a mano los nombres JSON que consume la interfaz: renombrar un
// campo en el servidor rompe esta prueba antes que la pantalla.
type metaContract struct {
	Statuses []struct {
		Status      string `json:"status"`
		Editable    bool   `json:"editable"`
		CanActivate bool   `json:"can_activate"`
		CanPause    bool   `json:"can_pause"`
		CanArchive  bool   `json:"can_archive"`
		Deletable   bool   `json:"deletable"`
	} `json:"statuses"`
	TriggerTypes []struct {
		Type           string `json:"type"`
		CampaignFilter bool   `json:"campaign_filter"`
		Date           bool   `json:"date"`
	} `json:"trigger_types"`
	StepTypes      []string `json:"step_types"`
	ConditionKinds []struct {
		Kind string `json:"kind"`
		Step bool   `json:"step"`
	} `json:"condition_kinds"`
	Wait struct {
		Units []struct {
			Unit    string `json:"unit"`
			Seconds int64  `json:"seconds"`
		} `json:"units"`
		MinSeconds int64 `json:"min_seconds"`
		MaxSeconds int64 `json:"max_seconds"`
	} `json:"wait"`
	RunStatuses       []string `json:"run_statuses"`
	DOIStatuses       []string `json:"doi_statuses"`
	PauseReasonManual string   `json:"pause_reason_manual"`
	TemplateKinds     struct {
		SendEmail   string `json:"send_email"`
		DoubleOptIn string `json:"double_opt_in"`
	} `json:"template_kinds"`
	DOILimits struct {
		PerDay    int `json:"per_day"`
		Per30Days int `json:"per_30_days"`
	} `json:"doi_limits"`
	Limits struct {
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
	} `json:"limits"`
	Pagination struct {
		DefaultPageSize int `json:"default_page_size"`
		MaxPageSize     int `json:"max_page_size"`
	} `json:"pagination"`
}

func names[T ~string](in []T) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = string(v)
	}
	return out
}

func TestMetaPublicaElCatalogoDelDominio(t *testing.T) {
	limits := domain.DOILimits{PerDay: 2, Per30Days: 5}
	uc := app.New(app.Deps{Templates: &apptest.Templates{}, Config: app.Config{DOILimits: limits}})
	guard := &recordingGuard{}
	r := chi.NewRouter()
	r.Use(middleware.InjectFromGateway)
	r.Mount("/api/v1/automations", NewHandler(uc, guard).Routes())

	req := httptest.NewRequest(nethttp.MethodGet, "/api/v1/automations/meta", nil)
	req.Header.Set("X-Tenant-ID", uuid.NewString())
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != nethttp.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if want := [3]string{permModule, "workflows", "read"}; len(guard.seen) != 1 || guard.seen[0] != want {
		t.Errorf("permiso exigido %v, esperado %v", guard.seen, want)
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
		w := domain.Workflow{Status: domain.Status(s.Status)}
		if s.Editable != w.Editable() || s.CanActivate != w.CanActivate() || s.CanPause != w.CanPause() ||
			s.CanArchive != w.CanArchive() || s.Deletable != w.Deletable() {
			t.Errorf("acciones de %s no siguen al dominio: %+v", s.Status, s)
		}
	}
	triggers := make([]string, 0, len(got.TriggerTypes))
	for _, tt := range got.TriggerTypes {
		triggers = append(triggers, tt.Type)
		if tt.CampaignFilter != domain.TriggerType(tt.Type).AcceptsCampaign() {
			t.Errorf("filtro de campana de %s", tt.Type)
		}
		if tt.Date != domain.TriggerType(tt.Type).IsDate() {
			t.Errorf("disparador por fecha %s", tt.Type)
		}
	}
	kinds := make([]string, 0, len(got.ConditionKinds))
	for _, k := range got.ConditionKinds {
		kinds = append(kinds, k.Kind)
		if k.Step != domain.ConditionKind(k.Kind).ReferencesStep() {
			t.Errorf("condicion %s", k.Kind)
		}
	}
	units := make([]string, 0, len(got.Wait.Units))
	for i, u := range got.Wait.Units {
		units = append(units, u.Unit)
		if want := domain.WaitUnits()[i]; u.Seconds != int64(want.Duration.Seconds()) {
			t.Errorf("unidad %s: %d s", u.Unit, u.Seconds)
		}
	}
	checks := []struct {
		name      string
		got, want any
	}{
		{"statuses", statuses, names(domain.Statuses())},
		{"trigger_types", triggers, names(domain.TriggerTypes())},
		{"step_types", got.StepTypes, names(domain.StepTypes())},
		{"condition_kinds", kinds, names(domain.ConditionKinds())},
		{"max_depth", got.Limits.MaxDepth, domain.MaxDepth},
		{"max_step_id_length", got.Limits.MaxStepIDLength, domain.MaxStepIDLen},
		{"max_trigger_hour", got.Limits.MaxTriggerHour, domain.MaxTriggerHour},
		{"max_condition_value_bytes", got.Limits.MaxConditionValue, domain.MaxConditionValueBytes},
		{"run_statuses", got.RunStatuses, names(domain.RunStatuses())},
		{"doi_statuses", got.DOIStatuses, names(domain.DOIStatuses())},
		{"wait.units", len(units), len(domain.WaitUnits())},
		{"wait.min", got.Wait.MinSeconds, int64(domain.MinWait.Seconds())},
		{"wait.max", got.Wait.MaxSeconds, int64(domain.MaxWait.Seconds())},
		{"pause_reason_manual", got.PauseReasonManual, domain.PauseReasonManual},
		{"template_kinds.send_email", got.TemplateKinds.SendEmail, app.KindMarketing},
		{"template_kinds.double_opt_in", got.TemplateKinds.DoubleOptIn, app.KindTransactional},
		{"doi_limits", [2]int{got.DOILimits.PerDay, got.DOILimits.Per30Days}, [2]int{limits.PerDay, limits.Per30Days}},
		{"min_steps", got.Limits.MinSteps, domain.MinSteps},
		{"max_steps", got.Limits.MaxSteps, domain.MaxSteps},
		{"max_name_length", got.Limits.MaxNameLength, domain.MaxNameLen},
		{"max_description_length", got.Limits.MaxDescriptionLength, domain.MaxDescriptionLen},
		{"max_pause_reason_length", got.Limits.MaxPauseReasonLength, domain.MaxPauseReasonLen},
		{"max_search_length", got.Limits.MaxSearchLength, maxSearchLen},
		{"pagination", [2]int{got.Pagination.DefaultPageSize, got.Pagination.MaxPageSize}, [2]int{defaultPerPage, maxPerPage}},
	}
	for _, c := range checks {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s = %v, quiero %v", c.name, c.got, c.want)
		}
	}
}

// Los topes que publica el catalogo son los que aplica el API: una espera en el limite
// pasa y una por encima no; una busqueda mas larga que el tope se rechaza.
func TestMetaCoincideConLoQueValidaElAPI(t *testing.T) {
	meta := buildMeta(domain.DOILimits{PerDay: 1, Per30Days: 3})
	units := meta.Wait.Units
	largest := units[len(units)-1]
	within := meta.Wait.MaxSeconds / largest.Seconds
	if _, err := domain.ParseWait(strconv.FormatInt(within, 10) + largest.Unit); err != nil {
		t.Errorf("la espera maxima publicada no se admite: %v", err)
	}
	if _, err := domain.ParseWait(strconv.FormatInt(within+1, 10) + largest.Unit); err == nil {
		t.Error("una espera por encima del maximo publicado se admite")
	}
	if _, err := domain.ParseWait("1" + units[0].Unit); meta.Wait.MinSeconds != units[0].Seconds || err != nil {
		t.Errorf("la espera minima publicada no coincide: %v", err)
	}

	s := newServer(t)
	long := strings.Repeat("a", meta.Limits.MaxSearchLength+1)
	if rec := s.do(nethttp.MethodGet, "/workflows?search="+long, ""); rec.Code != nethttp.StatusUnprocessableEntity {
		t.Errorf("busqueda por encima del tope: %d", rec.Code)
	}
}
