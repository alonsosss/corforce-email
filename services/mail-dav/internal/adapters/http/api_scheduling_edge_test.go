package http

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/services/mail-dav/internal/app/apptest"
)

// Sin planificacion configurada sus rutas responden 503, como un servicio que no esta: el webmail lo trata como
// una funcion apagada, no como un fallo de la peticion.
func TestAPIPlanificacionApagada(t *testing.T) {
	a := newAPIHarness(t, apptest.Binder{})
	tenant := ana.Principal.TenantID.String()
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/availability", map[string]any{"addresses": []string{"cris@acme.test"}, "start": "2026-09-24T00:00:00Z", "end": "2026-09-25T00:00:00Z"}},
		{http.MethodGet, "/booking", nil},
		{http.MethodPost, "/itip/inspect", map[string]any{"ical": "BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n"}},
	} {
		rec, env := a.call(ana, c.method, c.path, c.body, AddressHeader, "ana@acme.test")
		expectError(t, rec, env, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE")
	}
	rec, env := a.call(ana, http.MethodGet, "/booking/public/"+tenant+"/AAAAAAAAAAAAAAAAAAAAAAAA?start=2026-09-24T00:00:00Z&end=2026-09-25T00:00:00Z", nil,
		TenantHeader, "", MailboxHeader, "")
	expectError(t, rec, env, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE")
}

func TestAPIInvitacionesMalFormadas(t *testing.T) {
	a := newSchedulingHarness(t)
	rec, env := a.as(cris, http.MethodPost, "/itip/inspect", "{no es json")
	expectError(t, rec, env, http.StatusBadRequest, "BAD_REQUEST")

	rec, env = a.as(cris, http.MethodPost, "/itip/inspect", map[string]any{"ical": strings.Repeat("X", 9<<10)})
	expectError(t, rec, env, http.StatusRequestEntityTooLarge, "LIMIT_EXCEEDED")

	many := make([]string, maxITIPAddresses+1)
	for i := range many {
		many[i] = "cris@acme.test"
	}
	rec, env = a.as(cris, http.MethodPost, "/itip/respond", map[string]any{"ical": "BEGIN:VCALENDAR", "addresses": many, "response": "ACCEPTED"})
	expectError(t, rec, env, http.StatusUnprocessableEntity, "VALIDATION_ERROR")
	if env.Error.Details["field"] != "addresses" {
		t.Fatalf("campo: %+v", env.Error.Details)
	}
	rec, env = a.as(cris, http.MethodPost, "/itip/apply", map[string]any{"ical": "   ", "from": "ana@acme.test"})
	expectError(t, rec, env, http.StatusUnprocessableEntity, "VALIDATION_ERROR")
	if env.Error.Details["field"] != "ical" {
		t.Fatalf("campo: %+v", env.Error.Details)
	}

	rec, env = a.as(ana, http.MethodPost, "/calendar/events", map[string]any{
		"title": "Serie", "start": "2026-10-01T15:00:00Z", "end": "2026-10-01T15:30:00Z",
		"recurrence": map[string]any{"freq": "daily", "count": 3},
		"organizer":  map[string]string{"email": "ana@acme.test"},
		"attendees":  []map[string]string{{"email": "cris@acme.test"}},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("alta: %d %s", rec.Code, rec.Body.String())
	}
	ev := decodeData[meetingResp](t, env)
	rec, env = a.as(ana, http.MethodPost, "/calendar/events/"+ev.ID+"/itip", map[string]string{"method": "publish"})
	if rec.Code < 400 || rec.Code >= 500 {
		t.Fatalf("metodo desconocido: %d %s", rec.Code, rec.Body.String())
	}
	rec, env = a.as(ana, http.MethodPost, "/calendar/events/no-existe/itip", map[string]string{"method": "request"})
	expectError(t, rec, env, http.StatusNotFound, "NOT_FOUND")

	// Una cancelacion de una sola aparicion llega con su recurrence_id; el invitado aun no la tiene guardada.
	rec, env = a.as(ana, http.MethodPost, "/calendar/events/"+ev.ID+"/itip", map[string]string{"method": "cancel"})
	out := decodeData[struct {
		ICal string `json:"ical"`
	}](t, env)
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", rec.Code, rec.Body.String())
	}
	one := strings.Replace(out.ICal, "BEGIN:VEVENT\r\n", "BEGIN:VEVENT\r\nRECURRENCE-ID:20261002T150000Z\r\n", 1)
	rec, env = a.as(cris, http.MethodPost, "/itip/inspect", map[string]any{"ical": one, "addresses": []string{"cris@acme.test"}})
	state := decodeData[struct {
		Method       string  `json:"method"`
		RecurrenceID *string `json:"recurrence_id"`
		EventID      string  `json:"event_id"`
		Attendee     string  `json:"attendee"`
		IsOrganizer  bool    `json:"is_organizer"`
	}](t, env)
	if rec.Code != http.StatusOK || state.Method != "CANCEL" || state.RecurrenceID == nil || *state.RecurrenceID != "2026-10-02T15:00:00Z" ||
		state.EventID != "" || state.Attendee != "cris@acme.test" || state.IsOrganizer {
		t.Fatalf("cancelacion de una aparicion: %d %s", rec.Code, rec.Body.String())
	}
	// Aplicar lo que el buzon no tiene es un 404.
	rec, env = a.as(cris, http.MethodPost, "/itip/apply", map[string]any{"ical": one, "from": "ana@acme.test", "addresses": []string{"cris@acme.test"}})
	expectError(t, rec, env, http.StatusNotFound, "NOT_FOUND")
	// El organizador se reconoce como tal al inspeccionar su propia invitacion.
	rec, _ = a.as(ana, http.MethodPost, "/itip/inspect", map[string]any{"ical": out.ICal, "addresses": []string{"ana@acme.test"}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"is_organizer":true`) {
		t.Fatalf("organizador: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAPIFechasMalFormadasEnLaPlanificacion(t *testing.T) {
	a := newSchedulingHarness(t)
	for name, body := range map[string]map[string]any{
		"sin inicio":      {"addresses": []string{"cris@acme.test"}, "end": "2026-10-01T00:00:00Z"},
		"fin invalido":    {"addresses": []string{"cris@acme.test"}, "start": "2026-10-01T00:00:00Z", "end": "manana"},
		"sin direcciones": {"start": "2026-10-01T00:00:00Z", "end": "2026-10-02T00:00:00Z"},
	} {
		rec, env := a.as(ana, http.MethodPost, "/availability", body)
		if rec.Code != http.StatusUnprocessableEntity || env.Error == nil || env.Error.Code != "VALIDATION_ERROR" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	rec, env := a.as(ana, http.MethodPut, "/calendar/events/"+"00000000-0000-4000-8000-000000000000"+"/occurrences/"+url.PathEscape("no-es-fecha"),
		map[string]any{"title": "x", "start": "2026-10-01T15:00:00Z", "end": "2026-10-01T16:00:00Z"})
	expectError(t, rec, env, http.StatusUnprocessableEntity, "VALIDATION_ERROR")
}

func TestAPIConfiguracionDeCitas(t *testing.T) {
	a := newSchedulingHarness(t)
	base := func(weekly map[string]any) map[string]any {
		return map[string]any{"title": "Demo", "duration_minutes": 30, "max_advance_days": 14, "daily_limit": 5, "timezone": "UTC",
			"weekly": weekly, "active": true, "owner_name": "Ana"}
	}
	seven := make([]map[string]string, 7)
	for i := range seven {
		seven[i] = map[string]string{"start": "09:00", "end": "09:30"}
	}
	for name, weekly := range map[string]map[string]any{
		"demasiadas franjas": {"MO": seven},
		"hora imposible":     {"MO": []map[string]string{{"start": "25:00", "end": "26:00"}}},
		"hora sin formato":   {"MO": []map[string]string{{"start": "9", "end": "10:00"}}},
	} {
		rec, env := a.as(ana, http.MethodPut, "/booking", base(weekly))
		if rec.Code != http.StatusUnprocessableEntity || env.Error == nil || env.Error.Code != "VALIDATION_ERROR" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	rec, env := a.as(ana, http.MethodPut, "/booking", "[]")
	expectError(t, rec, env, http.StatusBadRequest, "BAD_REQUEST")

	weekly := map[string]any{"WE": []map[string]string{{"start": "09:00", "end": "11:00"}}}
	rec, env = a.as(ana, http.MethodPut, "/booking", base(weekly))
	type pageResp struct {
		PublicID string `json:"public_id"`
		Active   bool   `json:"active"`
	}
	first := decodeData[pageResp](t, env)
	if rec.Code != http.StatusOK || !first.Active {
		t.Fatalf("configurar: %d %s", rec.Code, rec.Body.String())
	}
	renew := base(weekly)
	renew["regenerate_link"] = true
	rec, env = a.as(ana, http.MethodPut, "/booking", renew)
	second := decodeData[pageResp](t, env)
	if rec.Code != http.StatusOK || second.PublicID == first.PublicID {
		t.Fatalf("enlace nuevo: %d %s", rec.Code, rec.Body.String())
	}
	rec, env = a.as(ana, http.MethodGet, "/booking", nil)
	if got := decodeData[pageResp](t, env); rec.Code != http.StatusOK || got.PublicID != second.PublicID {
		t.Fatalf("leer: %d %s", rec.Code, rec.Body.String())
	}

	public := func(method, path string, body any) (int, envelope) {
		rec, env := a.call(ana, method, "/booking/public/"+path, body, TenantHeader, "", MailboxHeader, "")
		return rec.Code, env
	}
	tenant := ana.Principal.TenantID.String()
	if code, _ := public(http.MethodGet, tenant+"/"+first.PublicID+"?start=2026-09-28T00:00:00Z&end=2026-10-05T00:00:00Z", nil); code != http.StatusNotFound {
		t.Fatalf("el enlace anterior sigue: %d", code)
	}
	if code, env := public(http.MethodGet, tenant+"/"+second.PublicID+"?end=2026-10-05T00:00:00Z", nil); code != http.StatusUnprocessableEntity || env.Error.Details["field"] != "start" {
		t.Fatalf("sin inicio: %d %+v", code, env.Error)
	}
	if code, _ := public(http.MethodPost, tenant+"/"+second.PublicID+"/reservations", "{"); code != http.StatusBadRequest {
		t.Fatalf("cuerpo invalido: %d", code)
	}
	if code, env := public(http.MethodPost, tenant+"/"+second.PublicID+"/reservations", map[string]string{"start": "pronto", "name": "Luis", "email": "luis@cliente.test"}); code != http.StatusUnprocessableEntity || env.Error.Details["field"] != "start" {
		t.Fatalf("inicio invalido: %d %+v", code, env.Error)
	}
	if code, _ := public(http.MethodPost, "no-uuid/"+second.PublicID+"/reservations", map[string]string{"start": "2026-09-30T09:00:00Z", "name": "Luis", "email": "luis@cliente.test"}); code != http.StatusNotFound {
		t.Fatalf("empresa invalida: %d", code)
	}
	code, env := public(http.MethodPost, tenant+"/"+second.PublicID+"/reservations", map[string]string{"start": "2026-09-30T09:00:00Z", "name": "Luis", "email": "luis@cliente.test"})
	res := decodeData[struct {
		EventID string `json:"event_id"`
		Start   string `json:"start"`
		End     string `json:"end"`
		Owner   struct {
			Email string `json:"email"`
			Name  string `json:"name"`
		} `json:"owner"`
	}](t, env)
	if code != http.StatusCreated || res.EventID == "" || res.Start != "2026-09-30T09:00:00Z" || res.End != "2026-09-30T09:30:00Z" || res.Owner.Email != "ana@acme.test" || res.Owner.Name != "Ana" {
		t.Fatalf("reserva: %d %+v", code, res)
	}
}
