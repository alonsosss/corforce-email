package http

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-dav/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-dav/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/mail-dav/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

var testAPILimits = APILimits{MaxBodyBytes: 8 << 10, MaxImportBytes: 16 << 10, MaxImportCards: 5, MaxWindowDays: 62, MaxOccurrences: 50, DefaultPerPage: 2, MaxPerPage: 3}

type apiHarness struct {
	t   *testing.T
	api http.Handler
	dav *harness
}

func newAPIHarness(t *testing.T, binder apptest.Binder) *apiHarness {
	t.Helper()
	auth, store := apptest.NewAuth(ana, bea, cris), apptest.NewStore()
	now := func() time.Time { return time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC) }
	uc, err := app.New(app.Deps{Auth: auth, Tenant: binder, Store: store, Calendars: store, Config: testConfig, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	api, err := NewAPI(uc, testAPILimits, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	davUC, _ := app.New(app.Deps{Auth: auth, Tenant: apptest.Binder{}, Store: store, Calendars: store, Config: testConfig})
	dav, err := NewHandler(davUC, Config{BasePath: base, Realm: "Contactos", MaxXMLBytes: 8 << 10})
	if err != nil {
		t.Fatal(err)
	}
	return &apiHarness{t: t, api: api.Routes(), dav: &harness{t: t, h: dav, auth: auth, store: store}}
}

type envelope struct {
	Data  json.RawMessage `json:"data"`
	Error *struct {
		Code    string            `json:"code"`
		Details map[string]string `json:"details"`
	} `json:"error"`
	Meta *struct {
		Page       int `json:"page"`
		PerPage    int `json:"per_page"`
		Total      int `json:"total"`
		TotalPages int `json:"total_pages"`
	} `json:"meta"`
}

// call llama a la API como el webmail en nombre de acc; headers anade o, con valor vacio, quita cabeceras.
func (a *apiHarness) call(acc apptest.Account, method, path string, body any, headers ...string) (*httptest.ResponseRecorder, envelope) {
	a.t.Helper()
	var reader *bytes.Reader
	contentType := "application/json"
	switch b := body.(type) {
	case nil:
		reader = bytes.NewReader(nil)
	case string:
		reader = bytes.NewReader([]byte(b))
	case *bytes.Buffer:
		reader = bytes.NewReader(b.Bytes())
	default:
		raw, _ := json.Marshal(b)
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, InternalPrefix+path, reader)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set(TenantHeader, acc.Principal.TenantID.String())
	req.Header.Set(MailboxHeader, acc.Principal.MailboxID.String())
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i+1] == "" {
			req.Header.Del(headers[i])
		} else {
			req.Header.Set(headers[i], headers[i+1])
		}
	}
	rec := httptest.NewRecorder()
	a.api.ServeHTTP(rec, req)
	var env envelope
	if strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			a.t.Fatalf("respuesta no es JSON: %q", rec.Body.String())
		}
	}
	return rec, env
}

func expectError(t *testing.T, rec *httptest.ResponseRecorder, env envelope, status int, code string) {
	t.Helper()
	if rec.Code != status || env.Error == nil || env.Error.Code != code {
		t.Fatalf("quiero %d %s, tengo %d %s", status, code, rec.Code, rec.Body.String())
	}
}

type contactResp struct {
	ID         string `json:"id"`
	ETag       string `json:"etag"`
	Name       string `json:"name"`
	GivenName  string `json:"given_name"`
	FamilyName string `json:"family_name"`
	Emails     []struct {
		Value string `json:"value"`
		Type  string `json:"type"`
	} `json:"emails"`
	Phones       []any  `json:"phones"`
	Organization string `json:"organization"`
	Notes        string `json:"notes"`
	Birthday     string `json:"birthday"`
	UpdatedAt    string `json:"updated_at"`
}

func decodeData[T any](t *testing.T, env envelope) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(env.Data, &out); err != nil {
		t.Fatalf("data: %v: %s", err, env.Data)
	}
	return out
}

func TestAPIExigeLaIdentidadDelBuzon(t *testing.T) {
	a := newAPIHarness(t, apptest.Binder{})
	for _, c := range []struct{ header, value string }{
		{TenantHeader, ""}, {MailboxHeader, ""}, {MailboxHeader, "no-es-uuid"}, {TenantHeader, uuid.Nil.String()},
	} {
		rec, env := a.call(ana, http.MethodGet, "/contacts", nil, c.header, c.value)
		expectError(t, rec, env, http.StatusBadRequest, "BAD_REQUEST")
		if env.Error.Details["field"] != c.header {
			t.Fatalf("%s=%q: detalle %v", c.header, c.value, env.Error.Details)
		}
	}
	rec, env := a.call(ana, http.MethodGet, "/meta", nil, TenantHeader, "", MailboxHeader, "")
	if rec.Code != http.StatusOK || !strings.Contains(string(env.Data), `"max_window_days":62`) || !strings.Contains(string(env.Data), `"max_import_cards":5`) {
		t.Fatalf("meta sin identidad: %d %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("la API no se cachea")
	}

	down := newAPIHarness(t, apptest.Binder{Err: domain.ErrUnavailable})
	rec, env = down.call(ana, http.MethodGet, "/contacts", nil)
	expectError(t, rec, env, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE")
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("el 503 lleva Retry-After")
	}
	rec, env = a.call(ana, http.MethodGet, "/nada", nil)
	expectError(t, rec, env, http.StatusNotFound, "NOT_FOUND")
}

func TestAPIContactosDeUnoEnUno(t *testing.T) {
	a := newAPIHarness(t, apptest.Binder{})
	in := map[string]any{
		"name": "Ana; Pérez", "given_name": "Ana", "family_name": "Pérez", "emails": []map[string]string{{"value": "ana@acme.test", "type": "work"}},
		"phones": []map[string]string{{"value": "+51 999", "type": "mobile"}}, "organization": "Acme", "notes": "uno\ndos", "birthday": "1985-04-12",
	}
	rec, env := a.call(ana, http.MethodPost, "/contacts", in)
	if rec.Code != http.StatusCreated {
		t.Fatalf("alta: %d %s", rec.Code, rec.Body.String())
	}
	created := decodeData[contactResp](t, env)
	if _, err := uuid.Parse(created.ID); err != nil || created.ETag == "" || rec.Header().Get("ETag") != `"`+created.ETag+`"` ||
		created.Name != "Ana; Pérez" || created.Notes != "uno\ndos" || created.Birthday != "1985-04-12" || created.UpdatedAt == "" {
		t.Fatalf("contacto creado: %+v", created)
	}

	// Lo que se crea por la API lo ve CardDAV.
	davRec := a.dav.req(http.MethodGet, base+"/addressbooks/ana@acme.test/contacts/"+created.ID+".vcf", "")
	expectStatus(t, davRec, http.StatusOK)
	if body := davRec.Body.String(); !strings.Contains(body, `FN:Ana\; Pérez`) || !strings.Contains(body, "UID:"+created.ID) {
		t.Fatalf("vCard por CardDAV:\n%s", body)
	}

	rec, env = a.call(ana, http.MethodGet, "/contacts/"+created.ID, nil)
	if got := decodeData[contactResp](t, env); rec.Code != http.StatusOK || got.ETag != created.ETag || got.Emails[0].Value != "ana@acme.test" {
		t.Fatalf("lectura: %d %+v", rec.Code, got)
	}
	// Otro buzon, aunque sea de la misma empresa, no lo ve.
	rec, env = a.call(cris, http.MethodGet, "/contacts/"+created.ID, nil)
	expectError(t, rec, env, http.StatusNotFound, "NOT_FOUND")

	in["title"] = "Gerente"
	rec, env = a.call(ana, http.MethodPut, "/contacts/"+created.ID, in, "If-Match", `"otro-etag"`)
	expectError(t, rec, env, http.StatusPreconditionFailed, "PRECONDITION_FAILED")
	rec, env = a.call(ana, http.MethodPut, "/contacts/"+created.ID, in, "If-Match", `"`+created.ETag+`"`)
	updated := decodeData[contactResp](t, env)
	if rec.Code != http.StatusOK || updated.ETag == created.ETag {
		t.Fatalf("actualizacion con If-Match: %d %s", rec.Code, rec.Body.String())
	}
	rec, _ = a.call(ana, http.MethodPut, "/contacts/"+created.ID, in)
	if rec.Code != http.StatusOK {
		t.Fatalf("actualizacion sin If-Match: %d %s", rec.Code, rec.Body.String())
	}
	rec, env = a.call(ana, http.MethodPut, "/contacts/no-existe", in)
	expectError(t, rec, env, http.StatusNotFound, "NOT_FOUND")
	rec, env = a.call(ana, http.MethodGet, "/contacts/..%2Fotro", nil)
	expectError(t, rec, env, http.StatusNotFound, "NOT_FOUND")

	rec, _ = a.call(ana, http.MethodDelete, "/contacts/"+created.ID, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("baja: %d %s", rec.Code, rec.Body.String())
	}
	rec, env = a.call(ana, http.MethodGet, "/contacts/"+created.ID, nil)
	expectError(t, rec, env, http.StatusNotFound, "NOT_FOUND")
}

func TestAPIContactosValidaLaEntrada(t *testing.T) {
	a := newAPIHarness(t, apptest.Binder{})
	rec, env := a.call(ana, http.MethodPost, "/contacts", map[string]any{"name": "Ana", "emails": []map[string]string{{"value": "ana"}}})
	expectError(t, rec, env, http.StatusUnprocessableEntity, "VALIDATION_ERROR")
	if env.Error.Details["field"] != "emails[0].value" {
		t.Fatalf("campo: %v", env.Error.Details)
	}
	rec, env = a.call(ana, http.MethodPost, "/contacts", map[string]any{"name": "Ana\r\nEMAIL:x@y.test"})
	expectError(t, rec, env, http.StatusUnprocessableEntity, "VALIDATION_ERROR")
	rec, env = a.call(ana, http.MethodPost, "/contacts", `{"name":"Ana","emails":"x"}`)
	expectError(t, rec, env, http.StatusUnprocessableEntity, "VALIDATION_ERROR")
	if env.Error.Details["field"] != "emails" {
		t.Fatalf("campo de tipo: %v", env.Error.Details)
	}
	rec, env = a.call(ana, http.MethodPost, "/contacts", `{"name":`)
	expectError(t, rec, env, http.StatusBadRequest, "BAD_REQUEST")
	rec, env = a.call(ana, http.MethodPost, "/contacts", `{"notes":"`+strings.Repeat("a", int(testAPILimits.MaxBodyBytes))+`"}`)
	expectError(t, rec, env, http.StatusRequestEntityTooLarge, "LIMIT_EXCEEDED")
	// Un contacto que no cabe en el tope de un vCard.
	rec, env = a.call(ana, http.MethodPost, "/contacts", map[string]any{"name": "Ana", "notes": strings.Repeat("a", 5000)})
	expectError(t, rec, env, http.StatusRequestEntityTooLarge, "LIMIT_EXCEEDED")
}

func TestAPIContactosBuscaPaginaYRespetaElTope(t *testing.T) {
	a := newAPIHarness(t, apptest.Binder{})
	for _, c := range []map[string]any{
		{"name": "Carla"}, {"name": "Beatriz", "emails": []map[string]string{{"value": "bea@beta.test"}}}, {"name": "alberto"}, {"name": "Dario"},
	} {
		if rec, _ := a.call(ana, http.MethodPost, "/contacts", c); rec.Code != http.StatusCreated {
			t.Fatalf("alta: %d %s", rec.Code, rec.Body.String())
		}
	}
	rec, env := a.call(ana, http.MethodPost, "/contacts", map[string]any{"name": "Quinto"})
	expectError(t, rec, env, http.StatusInsufficientStorage, "LIMIT_EXCEEDED")
	if env.Error.Details["limit"] != "contacts" {
		t.Fatalf("tope: %v", env.Error.Details)
	}

	rec, env = a.call(ana, http.MethodGet, "/contacts?page=1", nil)
	list := decodeData[[]contactResp](t, env)
	if rec.Code != http.StatusOK || len(list) != 2 || list[0].Name != "alberto" || list[1].Name != "Beatriz" ||
		env.Meta == nil || env.Meta.Total != 4 || env.Meta.TotalPages != 2 || env.Meta.PerPage != 2 {
		t.Fatalf("primera pagina: %s", rec.Body.String())
	}
	_, env = a.call(ana, http.MethodGet, "/contacts?page=2&per_page=3", nil)
	if list := decodeData[[]contactResp](t, env); len(list) != 1 || list[0].Name != "Dario" {
		t.Fatalf("segunda pagina: %+v", list)
	}
	_, env = a.call(ana, http.MethodGet, "/contacts?q=BETA.test", nil)
	if list := decodeData[[]contactResp](t, env); len(list) != 1 || list[0].Name != "Beatriz" || env.Meta.Total != 1 {
		t.Fatalf("busqueda por correo: %+v", list)
	}
	_, env = a.call(ana, http.MethodGet, "/contacts?q=AR", nil)
	if list := decodeData[[]contactResp](t, env); len(list) != 2 {
		t.Fatalf("busqueda por nombre: %+v", list)
	}
	_, env = a.call(ana, http.MethodGet, "/contacts?page=9", nil)
	if list := decodeData[[]contactResp](t, env); len(list) != 0 || string(env.Data) != "[]" {
		t.Fatalf("pagina vacia: %s", env.Data)
	}
	for _, q := range []string{"per_page=4", "per_page=0", "page=0", "page=x"} {
		rec, env = a.call(ana, http.MethodGet, "/contacts?"+q, nil)
		expectError(t, rec, env, http.StatusUnprocessableEntity, "VALIDATION_ERROR")
	}
	_, env = a.call(bea, http.MethodGet, "/contacts", nil)
	if list := decodeData[[]contactResp](t, env); len(list) != 0 {
		t.Fatalf("otra empresa: %+v", list)
	}
}

func multipartBody(t *testing.T, field, content string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	w, err := mw.CreateFormFile(field, "contactos.vcf")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte(content))
	_ = mw.Close()
	return &buf, mw.FormDataContentType()
}

func TestAPIImportaYExportaContactos(t *testing.T) {
	a := newAPIHarness(t, apptest.Binder{})
	rec, env := a.call(ana, http.MethodPost, "/contacts", map[string]any{"name": "Existente"})
	existing := decodeData[contactResp](t, env)
	if rec.Code != http.StatusCreated {
		t.Fatal(rec.Body.String())
	}
	file := strings.Join([]string{
		"BEGIN:VCARD\nVERSION:3.0\nFN:Sin UID\nEMAIL:nuevo@acme.test\nEND:VCARD",
		"BEGIN:VCARD\r\nVERSION:4.0\r\nUID:" + existing.ID + "\r\nFN:Existente renombrado\r\nX-CUSTOM:se guarda tal cual\r\nEND:VCARD",
		"BEGIN:VCARD\nVERSION:2.1\nUID:viejo\nFN:Antigua\nEND:VCARD",
		"BEGIN:VCARD\nVERSION:4.0\nUID:roto\nFN:Sin cerrar",
	}, "\r\n")
	body, ctype := multipartBody(t, "file", file)
	rec, env = a.call(ana, http.MethodPost, "/contacts/import", body, "Content-Type", ctype)
	res := decodeData[importJSON](t, env)
	if rec.Code != http.StatusOK || res.Imported != 1 || res.Updated != 1 || len(res.Skipped) != 2 || res.Skipped[0].Index != 2 || res.Skipped[1].Index != 3 || res.Skipped[0].Reason == "" {
		t.Fatalf("importacion: %d %s", rec.Code, rec.Body.String())
	}
	_, env = a.call(ana, http.MethodGet, "/contacts/"+existing.ID, nil)
	if got := decodeData[contactResp](t, env); got.Name != "Existente renombrado" {
		t.Fatalf("el UID existente se actualiza: %+v", got)
	}

	rec, _ = a.call(ana, http.MethodGet, "/contacts/export", nil)
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/vcard") ||
		strings.Count(rec.Body.String(), "BEGIN:VCARD") != 2 || !strings.Contains(rec.Body.String(), "X-CUSTOM:se guarda tal cual") {
		t.Fatalf("exportacion: %d %q", rec.Code, rec.Body.String())
	}
	cards, err := domain.SplitVCards(rec.Body.String(), 10)
	if err != nil || len(cards) != 2 {
		t.Fatalf("la exportacion se vuelve a importar: %v %d", err, len(cards))
	}
	rec, _ = a.call(cris, http.MethodGet, "/contacts/export", nil)
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("libreta vacia: %d %q", rec.Code, rec.Body.String())
	}

	many := strings.Repeat("BEGIN:VCARD\nVERSION:4.0\nFN:x\nEND:VCARD\n", testAPILimits.MaxImportCards+1)
	body, ctype = multipartBody(t, "file", many)
	rec, env = a.call(ana, http.MethodPost, "/contacts/import", body, "Content-Type", ctype)
	expectError(t, rec, env, http.StatusRequestEntityTooLarge, "LIMIT_EXCEEDED")
	if env.Error.Details["limit"] != "import_cards" {
		t.Fatalf("tope de tarjetas: %v", env.Error.Details)
	}
	body, ctype = multipartBody(t, "file", strings.Repeat("x", int(testAPILimits.MaxImportBytes)+1))
	rec, env = a.call(ana, http.MethodPost, "/contacts/import", body, "Content-Type", ctype)
	expectError(t, rec, env, http.StatusRequestEntityTooLarge, "LIMIT_EXCEEDED")
	body, ctype = multipartBody(t, "otro", "BEGIN:VCARD\nEND:VCARD")
	rec, env = a.call(ana, http.MethodPost, "/contacts/import", body, "Content-Type", ctype)
	expectError(t, rec, env, http.StatusUnprocessableEntity, "VALIDATION_ERROR")
	body, ctype = multipartBody(t, "file", "no es un vcf")
	rec, env = a.call(ana, http.MethodPost, "/contacts/import", body, "Content-Type", ctype)
	expectError(t, rec, env, http.StatusUnprocessableEntity, "VALIDATION_ERROR")
	rec, env = a.call(ana, http.MethodPost, "/contacts/import", "{}")
	expectError(t, rec, env, http.StatusUnprocessableEntity, "VALIDATION_ERROR")
}

type eventResp struct {
	ID         string `json:"id"`
	ETag       string `json:"etag"`
	Title      string `json:"title"`
	Start      string `json:"start"`
	End        string `json:"end"`
	AllDay     bool   `json:"all_day"`
	Location   string `json:"location"`
	Recurrence *struct {
		Freq     string   `json:"freq"`
		Interval int      `json:"interval"`
		Count    *int     `json:"count"`
		Until    *string  `json:"until"`
		ByDay    []string `json:"by_day"`
	} `json:"recurrence"`
	ReminderMinutes *int `json:"reminder_minutes"`
}

type occurrenceResp struct {
	ID        string `json:"id"`
	Start     string `json:"start"`
	End       string `json:"end"`
	AllDay    bool   `json:"all_day"`
	Title     string `json:"title"`
	Recurring bool   `json:"recurring"`
}

func TestAPIEventos(t *testing.T) {
	a := newAPIHarness(t, apptest.Binder{})
	in := map[string]any{
		"title": "Comite", "start": "2026-10-05T09:00:00-05:00", "end": "2026-10-05T10:00:00-05:00", "location": "Sala 1",
		"recurrence": map[string]any{"freq": "weekly", "interval": 1, "count": nil, "until": "2026-10-31T23:59:59Z", "by_day": []string{"MO", "TH"}}, "reminder_minutes": 15,
	}
	rec, env := a.call(ana, http.MethodPost, "/calendar/events", in)
	ev := decodeData[eventResp](t, env)
	if rec.Code != http.StatusCreated || ev.Start != "2026-10-05T14:00:00Z" || ev.End != "2026-10-05T15:00:00Z" || ev.Recurrence == nil ||
		ev.Recurrence.Freq != "weekly" || ev.Recurrence.Count != nil || *ev.Recurrence.Until != "2026-10-31T23:59:59Z" || *ev.ReminderMinutes != 15 {
		t.Fatalf("alta: %d %s", rec.Code, rec.Body.String())
	}
	davRec := a.dav.req(http.MethodGet, base+"/calendars/ana@acme.test/calendar/"+ev.ID+".ics", "")
	expectStatus(t, davRec, http.StatusOK)
	if !strings.Contains(davRec.Body.String(), "RRULE:FREQ=WEEKLY;UNTIL=20261031T235959Z;BYDAY=MO,TH") {
		t.Fatalf("iCalendar por CalDAV:\n%s", davRec.Body.String())
	}

	rec, env = a.call(ana, http.MethodGet, "/calendar/events?start=2026-10-01T00:00:00Z&end=2026-11-01T00:00:00Z", nil)
	occs := decodeData[[]occurrenceResp](t, env)
	if rec.Code != http.StatusOK || len(occs) != 8 || occs[0].Start != "2026-10-05T14:00:00Z" || occs[1].Start != "2026-10-08T14:00:00Z" ||
		!occs[0].Recurring || occs[0].ID != ev.ID || occs[0].Title != "Comite" {
		t.Fatalf("apariciones: %d %s", rec.Code, rec.Body.String())
	}

	// Un evento de un cliente CalDAV con una excepcion tambien aparece, sin la fecha excluida.
	ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Prueba//ES\r\nBEGIN:VEVENT\r\nUID:movil-1\r\nDTSTAMP:20260101T000000Z\r\n" +
		"DTSTART;VALUE=DATE:20261010\r\nRRULE:FREQ=DAILY;COUNT=3\r\nEXDATE;VALUE=DATE:20261011\r\nSUMMARY:Viaje\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	expectStatus(t, a.dav.req(http.MethodPut, base+"/calendars/ana@acme.test/calendar/movil-1.ics", ics), http.StatusCreated)
	_, env = a.call(ana, http.MethodGet, "/calendar/events?start=2026-10-09T00:00:00Z&end=2026-10-13T00:00:00Z", nil)
	occs = decodeData[[]occurrenceResp](t, env)
	if len(occs) != 3 || occs[0].ID != "movil-1" || !occs[0].AllDay || occs[0].Start != "2026-10-10T00:00:00Z" || occs[0].End != "2026-10-11T00:00:00Z" ||
		occs[1].Start != "2026-10-12T00:00:00Z" || occs[2].ID != ev.ID {
		t.Fatalf("con excepcion: %+v", occs)
	}

	for q, field := range map[string]string{
		"start=2026-10-01T00:00:00Z&end=2026-12-15T00:00:00Z": "end",
		"start=2026-10-02T00:00:00Z&end=2026-10-01T00:00:00Z": "end",
		"end=2026-10-01T00:00:00Z":                            "start",
		"start=2026-10-01&end=2026-10-02T00:00:00Z":           "start",
	} {
		rec, env = a.call(ana, http.MethodGet, "/calendar/events?"+q, nil)
		expectError(t, rec, env, http.StatusUnprocessableEntity, "VALIDATION_ERROR")
		if env.Error.Details["field"] != field {
			t.Fatalf("%s: %v", q, env.Error.Details)
		}
	}
	if env.Error.Details["max_days"] != "" {
		t.Fatal("max_days solo acompana a la ventana excesiva")
	}
	rec, env = a.call(ana, http.MethodGet, "/calendar/events?start=2026-10-01T00:00:00Z&end=2026-12-15T00:00:00Z", nil)
	if env.Error.Details["max_days"] != strconv.Itoa(testAPILimits.MaxWindowDays) {
		t.Fatalf("ventana: %v", env.Error.Details)
	}

	rec, env = a.call(ana, http.MethodGet, "/calendar/events/"+ev.ID, nil)
	if got := decodeData[eventResp](t, env); rec.Code != http.StatusOK || got.ETag != ev.ETag || got.Location != "Sala 1" {
		t.Fatalf("lectura: %s", rec.Body.String())
	}
	in["title"] = "Comite de direccion"
	in["recurrence"] = nil
	in["reminder_minutes"] = nil
	rec, env = a.call(ana, http.MethodPut, "/calendar/events/"+ev.ID, in, "If-Match", `"viejo"`)
	expectError(t, rec, env, http.StatusPreconditionFailed, "PRECONDITION_FAILED")
	rec, env = a.call(ana, http.MethodPut, "/calendar/events/"+ev.ID, in, "If-Match", `"`+ev.ETag+`"`)
	if got := decodeData[eventResp](t, env); rec.Code != http.StatusOK || got.Title != "Comite de direccion" || got.Recurrence != nil || got.ReminderMinutes != nil {
		t.Fatalf("actualizacion: %d %s", rec.Code, rec.Body.String())
	}
	rec, env = a.call(ana, http.MethodPost, "/calendar/events", map[string]any{"title": "x", "start": "2026-10-05T09:00:00Z", "end": "2026-10-05T08:00:00Z"})
	expectError(t, rec, env, http.StatusUnprocessableEntity, "VALIDATION_ERROR")
	rec, env = a.call(ana, http.MethodPost, "/calendar/events", map[string]any{"title": "x", "start": "2026-10-05", "end": "2026-10-05", "all_day": true,
		"recurrence": map[string]any{"freq": "daily", "count": 0}})
	expectError(t, rec, env, http.StatusUnprocessableEntity, "VALIDATION_ERROR")
	if env.Error.Details["field"] != "recurrence.count" {
		t.Fatalf("count: %v", env.Error.Details)
	}
	rec, env = a.call(ana, http.MethodPost, "/calendar/events", map[string]any{"title": "Feriado", "start": "2026-10-05", "end": "2026-10-05", "all_day": true})
	if got := decodeData[eventResp](t, env); rec.Code != http.StatusCreated || !got.AllDay || got.Start != "2026-10-05T00:00:00Z" || got.End != "2026-10-06T00:00:00Z" {
		t.Fatalf("dia completo: %d %s", rec.Code, rec.Body.String())
	}

	rec, _ = a.call(ana, http.MethodDelete, "/calendar/events/"+ev.ID, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("baja: %d", rec.Code)
	}
	rec, env = a.call(ana, http.MethodGet, "/calendar/events/"+ev.ID, nil)
	expectError(t, rec, env, http.StatusNotFound, "NOT_FOUND")
	rec, env = a.call(bea, http.MethodGet, "/calendar/events/movil-1", nil)
	expectError(t, rec, env, http.StatusNotFound, "NOT_FOUND")
}

// Mas apariciones de las que admite una respuesta es un error explicito, no una lista cortada en silencio.
func TestAPIAparicionesConTope(t *testing.T) {
	a := newAPIHarness(t, apptest.Binder{})
	in := map[string]any{"title": "Diario", "start": "2026-10-01T08:00:00Z", "end": "2026-10-01T08:30:00Z", "recurrence": map[string]any{"freq": "daily"}}
	if rec, _ := a.call(ana, http.MethodPost, "/calendar/events", in); rec.Code != http.StatusCreated {
		t.Fatal(rec.Body.String())
	}
	rec, env := a.call(ana, http.MethodGet, "/calendar/events?start=2026-10-01T00:00:00Z&end=2026-12-01T00:00:00Z", nil)
	expectError(t, rec, env, http.StatusInsufficientStorage, "LIMIT_EXCEEDED")
	if env.Error.Details["limit"] != "result" {
		t.Fatalf("tope: %v", env.Error.Details)
	}
}
