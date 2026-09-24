package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-dav/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-dav/internal/app/apptest"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

var schedulingNow = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

func newSchedulingHarness(t *testing.T) *apiHarness {
	t.Helper()
	store := apptest.NewStore()
	store.Now = func() time.Time { return schedulingNow }
	cfg := testConfig
	cfg.Scheduling = app.SchedulingConfig{
		BusyHorizon: 400 * 24 * time.Hour, BusyLookback: 7 * 24 * time.Hour, MaxBusyPerEvent: 100, BusyRefreshMax: 50,
		MaxAvailabilityAddresses: 5, BookingMaxDaily: 20, BookingMaxPerVisitor: 1, BookingMaxSlots: 50, BookingMaxWindow: 14 * 24 * time.Hour,
	}
	uc, err := app.New(app.Deps{Auth: apptest.NewAuth(ana, bea, cris), Tenant: apptest.Binder{}, Store: store, Calendars: store,
		Scheduling: apptest.NewScheduling(store), Config: cfg, Now: func() time.Time { return schedulingNow }})
	if err != nil {
		t.Fatal(err)
	}
	api, err := NewAPI(uc, testAPILimits, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	return &apiHarness{t: t, api: api.Routes()}
}

func (a *apiHarness) as(acc apptest.Account, method, path string, body any) (*httptest.ResponseRecorder, envelope) {
	a.t.Helper()
	return a.call(acc, method, path, body, AddressHeader, acc.Principal.Username)
}

type meetingResp struct {
	ID        string `json:"id"`
	ETag      string `json:"etag"`
	Title     string `json:"title"`
	Start     string `json:"start"`
	TimeZone  string `json:"timezone"`
	Organizer *struct {
		Email string `json:"email"`
	} `json:"organizer"`
	Attendees []struct {
		Email    string `json:"email"`
		PartStat string `json:"partstat"`
	} `json:"attendees"`
}

func TestAPIZonaYAparicion(t *testing.T) {
	a := newSchedulingHarness(t)
	rec, env := a.as(ana, http.MethodPost, "/calendar/events", map[string]any{
		"title": "Diario", "start": "2026-10-01T15:00:00Z", "end": "2026-10-01T15:30:00Z", "timezone": "America/Lima",
		"recurrence": map[string]any{"freq": "daily", "count": 3},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("alta: %d %s", rec.Code, rec.Body.String())
	}
	ev := decodeData[meetingResp](t, env)
	if ev.TimeZone != "America/Lima" {
		t.Fatalf("zona: %+v", ev)
	}
	rec, env = a.as(ana, http.MethodGet, "/calendar/events?start=2026-10-01T00:00:00Z&end=2026-10-05T00:00:00Z", nil)
	occs := decodeData[[]struct {
		RecurrenceID string `json:"recurrence_id"`
		Title        string `json:"title"`
	}](t, env)
	if rec.Code != http.StatusOK || len(occs) != 3 || occs[1].RecurrenceID != "2026-10-02T15:00:00Z" {
		t.Fatalf("apariciones: %d %s", rec.Code, rec.Body.String())
	}
	rid := url.PathEscape(occs[1].RecurrenceID)
	rec, env = a.as(ana, http.MethodPut, "/calendar/events/"+ev.ID+"/occurrences/"+rid, map[string]any{
		"title": "Solo hoy", "start": "2026-10-02T17:00:00Z", "end": "2026-10-02T17:30:00Z",
	})
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") == "" {
		t.Fatalf("editar aparicion: %d %s", rec.Code, rec.Body.String())
	}
	rec, env = a.as(ana, http.MethodDelete, "/calendar/events/"+ev.ID+"/occurrences/"+url.PathEscape("2026-10-03T15:00:00Z"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("borrar aparicion: %d %s", rec.Code, rec.Body.String())
	}
	rec, env = a.as(ana, http.MethodDelete, "/calendar/events/"+ev.ID+"/occurrences/"+url.PathEscape("2026-10-03T15:00:00Z"), nil)
	expectError(t, rec, env, http.StatusNotFound, "NOT_FOUND")
	rec, env = a.as(ana, http.MethodDelete, "/calendar/events/"+ev.ID+"/occurrences/ayer", nil)
	expectError(t, rec, env, http.StatusUnprocessableEntity, "VALIDATION_ERROR")
	rec, _ = a.as(ana, http.MethodGet, "/calendar/events?start=2026-10-01T00:00:00Z&end=2026-10-05T00:00:00Z", nil)
	if !strings.Contains(rec.Body.String(), "Solo hoy") || strings.Count(rec.Body.String(), `"recurrence_id"`) != 2 {
		t.Fatalf("despues: %s", rec.Body.String())
	}
	rec, env = a.as(ana, http.MethodPost, "/calendar/events", map[string]any{"title": "x", "start": "2026-10-01T15:00:00Z", "end": "2026-10-01T16:00:00Z", "timezone": "Luna/Base"})
	expectError(t, rec, env, http.StatusUnprocessableEntity, "VALIDATION_ERROR")
	if env.Error.Details["field"] != "timezone" {
		t.Fatalf("campo: %+v", env.Error.Details)
	}
}

func TestAPIInvitacionYDisponibilidad(t *testing.T) {
	a := newSchedulingHarness(t)
	rec, env := a.as(ana, http.MethodPost, "/calendar/events", map[string]any{
		"title": "Contrato confidencial", "start": "2026-10-01T15:00:00Z", "end": "2026-10-01T16:00:00Z",
		"organizer": map[string]string{"email": "ana@acme.test", "name": "Ana"},
		"attendees": []map[string]string{{"email": "cris@acme.test"}},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("alta: %d %s", rec.Code, rec.Body.String())
	}
	ev := decodeData[meetingResp](t, env)
	if ev.Organizer == nil || len(ev.Attendees) != 1 || ev.Attendees[0].PartStat != "NEEDS-ACTION" {
		t.Fatalf("reunion: %+v", ev)
	}
	rec, env = a.as(ana, http.MethodPost, "/calendar/events/"+ev.ID+"/itip", map[string]string{"method": "request"})
	out := decodeData[struct {
		Method     string   `json:"method"`
		ICal       string   `json:"ical"`
		Recipients []string `json:"recipients"`
	}](t, env)
	if rec.Code != http.StatusOK || out.Method != "REQUEST" || len(out.Recipients) != 1 {
		t.Fatalf("itip: %d %s", rec.Code, rec.Body.String())
	}

	rec, env = a.as(cris, http.MethodPost, "/itip/inspect", map[string]any{"ical": out.ICal, "addresses": []string{"cris@acme.test"}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"attendee":"cris@acme.test"`) {
		t.Fatalf("inspeccion: %d %s", rec.Code, rec.Body.String())
	}
	rec, env = a.as(cris, http.MethodPost, "/itip/respond", map[string]any{"ical": out.ICal, "addresses": []string{"cris@acme.test"}, "response": "ACCEPTED"})
	ans := decodeData[struct {
		Reply   string `json:"reply"`
		EventID string `json:"event_id"`
	}](t, env)
	if rec.Code != http.StatusOK || ans.EventID == "" || !strings.Contains(ans.Reply, "METHOD:REPLY") {
		t.Fatalf("respuesta: %d %s", rec.Code, rec.Body.String())
	}
	rec, env = a.as(ana, http.MethodPost, "/itip/apply", map[string]any{"ical": ans.Reply, "from": "cris@acme.test", "addresses": []string{"ana@acme.test"}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"changed":true`) {
		t.Fatalf("aplicar: %d %s", rec.Code, rec.Body.String())
	}
	rec, env = a.as(ana, http.MethodPost, "/itip/inspect", map[string]any{"ical": "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nEND:VCALENDAR\r\n"})
	expectError(t, rec, env, http.StatusUnprocessableEntity, "VALIDATION_ERROR")
	rec, env = a.as(ana, http.MethodPost, "/itip/inspect", map[string]any{"ical": ""})
	expectError(t, rec, env, http.StatusUnprocessableEntity, "VALIDATION_ERROR")

	// La disponibilidad de ana, vista por cris, es solo inicio y fin: ni el titulo ni los invitados.
	rec, env = a.as(cris, http.MethodPost, "/availability", map[string]any{"addresses": []string{"ana@acme.test", "bea@beta.test"},
		"start": "2026-09-28T00:00:00Z", "end": "2026-10-05T00:00:00Z"})
	if rec.Code != http.StatusOK {
		t.Fatalf("disponibilidad: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, secret := range []string{"Contrato", "confidencial"} {
		if strings.Contains(body, secret) {
			t.Fatalf("la disponibilidad filtra %q: %s", secret, body)
		}
	}
	list := decodeData[[]struct {
		Address string `json:"address"`
		Known   bool   `json:"known"`
		Busy    []struct {
			Start string `json:"start"`
			End   string `json:"end"`
		} `json:"busy"`
	}](t, env)
	if len(list) != 2 || !list[0].Known || len(list[0].Busy) != 1 || list[0].Busy[0].Start != "2026-10-01T15:00:00Z" || list[1].Known {
		t.Fatalf("disponibilidad: %s", body)
	}
	var raw []map[string]any
	_ = json.Unmarshal(env.Data, &raw)
	for key := range raw[0] {
		if key != "address" && key != "known" && key != "partial" && key != "busy" {
			t.Fatalf("campo inesperado %q en la disponibilidad", key)
		}
	}
	rec, env = a.call(cris, http.MethodPost, "/availability", map[string]any{"addresses": []string{"ana@acme.test"}, "start": "2026-09-28T00:00:00Z", "end": "2026-10-05T00:00:00Z"},
		AddressHeader, "no es una direccion")
	expectError(t, rec, env, http.StatusUnprocessableEntity, "VALIDATION_ERROR")
}

func TestAPIPaginaDeCitas(t *testing.T) {
	a := newSchedulingHarness(t)
	weekly := map[string]any{"MO": []map[string]string{}, "TU": []map[string]string{{"start": "09:00", "end": "10:00"}}}
	settings := map[string]any{"title": "Demo", "duration_minutes": 30, "max_advance_days": 14, "daily_limit": 5, "timezone": "UTC",
		"weekly": weekly, "active": true, "owner_name": "Ana"}
	rec, env := a.as(ana, http.MethodGet, "/booking", nil)
	expectError(t, rec, env, http.StatusNotFound, "NOT_FOUND")
	rec, env = a.as(ana, http.MethodPut, "/booking", settings)
	if rec.Code != http.StatusOK {
		t.Fatalf("configurar: %d %s", rec.Code, rec.Body.String())
	}
	page := decodeData[struct {
		PublicID     string                         `json:"public_id"`
		OwnerAddress string                         `json:"owner_address"`
		Weekly       map[string][]map[string]string `json:"weekly"`
	}](t, env)
	if page.OwnerAddress != "ana@acme.test" || len(page.Weekly["TU"]) != 1 || len(page.Weekly) != 7 {
		t.Fatalf("pagina: %s", rec.Body.String())
	}
	bad := map[string]any{"title": "Demo", "duration_minutes": 30, "max_advance_days": 14, "daily_limit": 5, "timezone": "UTC",
		"weekly": map[string]any{"XX": []map[string]string{}}, "active": true}
	rec, env = a.as(ana, http.MethodPut, "/booking", bad)
	expectError(t, rec, env, http.StatusUnprocessableEntity, "VALIDATION_ERROR")

	public := func(method, path string, body any) (*httptest.ResponseRecorder, envelope) {
		return a.call(ana, method, "/booking/public/"+path, body, TenantHeader, "", MailboxHeader, "")
	}
	tenant := ana.Principal.TenantID.String()
	rec, env = public(http.MethodGet, tenant+"/"+page.PublicID+"?start=2026-09-28T00:00:00Z&end=2026-10-05T00:00:00Z", nil)
	slots := decodeData[struct {
		Title string `json:"title"`
		Slots []struct {
			Start string `json:"start"`
		} `json:"slots"`
	}](t, env)
	if rec.Code != http.StatusOK || slots.Title != "Demo" || len(slots.Slots) != 2 || slots.Slots[0].Start != "2026-09-29T09:00:00Z" {
		t.Fatalf("huecos: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "ana@acme.test") {
		t.Fatalf("la pagina publica expone la direccion del dueno: %s", rec.Body.String())
	}
	for _, path := range []string{uuid.NewString() + "/" + page.PublicID, tenant + "/corto", "no-uuid/" + page.PublicID} {
		rec, env = public(http.MethodGet, path+"?start=2026-09-28T00:00:00Z&end=2026-10-05T00:00:00Z", nil)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
	rec, env = public(http.MethodPost, tenant+"/"+page.PublicID+"/reservations", map[string]string{
		"start": "2026-09-29T09:00:00Z", "name": "Luis", "email": "luis@cliente.test", "note": "hola"})
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"recipients":["luis@cliente.test"]`) || strings.Contains(rec.Body.String(), "hola") {
		t.Fatalf("reservar: %d %s", rec.Code, rec.Body.String())
	}
	rec, env = public(http.MethodPost, tenant+"/"+page.PublicID+"/reservations", map[string]string{
		"start": "2026-09-29T09:00:00Z", "name": "Eva", "email": "eva@cliente.test"})
	expectError(t, rec, env, http.StatusConflict, "SLOT_UNAVAILABLE")
	rec, env = public(http.MethodPost, tenant+"/"+page.PublicID+"/reservations", map[string]string{
		"start": "2026-09-29T09:30:00Z", "name": "Luis", "email": "luis@cliente.test"})
	expectError(t, rec, env, http.StatusTooManyRequests, "LIMIT_EXCEEDED")
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("sin Retry-After")
	}
	rec, env = public(http.MethodPost, tenant+"/"+page.PublicID+"/reservations", map[string]string{
		"start": "2026-09-29T09:30:00Z", "name": "Eva", "email": "no-es-correo"})
	expectError(t, rec, env, http.StatusUnprocessableEntity, "VALIDATION_ERROR")
}
