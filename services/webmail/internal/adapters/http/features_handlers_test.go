package http

import (
	"bytes"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

var jsonOrigin = map[string]string{"Origin": allowedOrigin, "Content-Type": "application/json"}

func body(s string) *strings.Reader { return strings.NewReader(s) }

func TestLasRutasNuevasExigenSesionYOrigen(t *testing.T) {
	env := newTestEnv(t, nopSender{})
	cookie := login(t, env.h)
	writes := []struct{ method, path, body string }{
		{http.MethodPost, "/folders", `{"name":"X"}`},
		{http.MethodPatch, "/folders/Viejos", `{"name":"X"}`},
		{http.MethodDelete, "/folders/Viejos", ""},
		{http.MethodPost, "/folders/Trash/empty", ""},
		{http.MethodPost, "/folders/INBOX/messages/batch", `{"uids":[1],"action":"delete"}`},
		{http.MethodPut, "/signature", `{"enabled":true,"html":"x","on_replies":false}`},
		{http.MethodPut, "/filters", `{"rules":[],"forwarding":{"enabled":false,"addresses":[],"keep_copy":false}}`},
		{http.MethodPost, "/password", `{"current_password":"a","new_password":"b"}`},
		{http.MethodPatch, "/scheduled/00000000-0000-4000-8000-000000000001", `{"send_at":"2030-01-01T00:00:00Z"}`},
		{http.MethodDelete, "/scheduled/00000000-0000-4000-8000-000000000001", ""},
		{http.MethodPost, "/contacts", `{}`},
		{http.MethodPut, "/contacts/c1", `{}`},
		{http.MethodDelete, "/contacts/c1", ""},
		{http.MethodPost, "/contacts/import", ""},
		{http.MethodPost, "/calendar/events", `{}`},
		{http.MethodPut, "/calendar/events/e1", `{}`},
		{http.MethodDelete, "/calendar/events/e1", ""},
	}
	for _, w := range writes {
		rec := do(env.h, w.method, BasePath+w.path, body(w.body), map[string]string{"Content-Type": "application/json"}, cookie)
		if rec.Code != http.StatusForbidden || errorCode(t, rec) != "ORIGIN_NOT_ALLOWED" {
			t.Errorf("%s %s sin Origin: %d %s", w.method, w.path, rec.Code, rec.Body)
		}
		rec = do(env.h, w.method, BasePath+w.path, body(w.body), jsonOrigin, nil)
		if rec.Code != http.StatusUnauthorized || errorCode(t, rec) != "SESSION_EXPIRED" {
			t.Errorf("%s %s sin sesion: %d %s", w.method, w.path, rec.Code, rec.Body)
		}
	}
	for _, path := range []string{"/signature", "/filters", "/scheduled", "/contacts", "/contacts/export", "/contacts/c1",
		"/calendar/events?start=2026-09-01T00:00:00Z&end=2026-09-30T00:00:00Z", "/calendar/events/e1", "/folders/INBOX/messages/1/raw", "/meta/dav"} {
		if rec := do(env.h, http.MethodGet, BasePath+path, nil, nil, nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s sin sesion: %d", path, rec.Code)
		}
	}
	if env.dav.calls != 0 || env.settings.username != "" {
		t.Fatal("nada llega a los servicios sin sesion u origen")
	}
}

func TestCarpetasPorHTTP(t *testing.T) {
	env := newTestEnv(t, nopSender{})
	cookie := login(t, env.h)

	rec := do(env.h, http.MethodPost, BasePath+"/folders", body(`{"name":"Clientes/2027"}`), jsonOrigin, cookie)
	if rec.Code != http.StatusCreated {
		t.Fatalf("crear: %d %s", rec.Code, rec.Body)
	}
	var f folderDTO
	envelopeData(t, rec, &f)
	if f.Name != "Clientes/2027" || f.Delimiter != "/" || f.Role != "" || !f.Selectable {
		t.Fatalf("carpeta: %+v", f)
	}
	cases := []struct {
		method, path, body string
		status             int
		code               string
	}{
		{http.MethodPost, "/folders", `{"name":"Viejos"}`, http.StatusConflict, "FOLDER_EXISTS"},
		{http.MethodPost, "/folders", `{"name":"INBOX"}`, http.StatusUnprocessableEntity, "VALIDATION_ERROR"},
		{http.MethodPost, "/folders", `{"name":"x","extra":1}`, http.StatusBadRequest, "BAD_REQUEST"},
		{http.MethodPatch, "/folders/Sent", `{"name":"Otra"}`, http.StatusConflict, "FOLDER_PROTECTED"},
		{http.MethodPatch, "/folders/INBOX", `{"name":"Otra"}`, http.StatusConflict, "FOLDER_PROTECTED"},
		{http.MethodPatch, "/folders/Viejos", `{"name":"Clientes"}`, http.StatusConflict, "FOLDER_EXISTS"},
		{http.MethodDelete, "/folders/Clientes", "", http.StatusConflict, "FOLDER_HAS_CHILDREN"},
		{http.MethodDelete, "/folders/Trash", "", http.StatusConflict, "FOLDER_PROTECTED"},
		{http.MethodDelete, "/folders/NoEsta", "", http.StatusNotFound, "FOLDER_NOT_FOUND"},
		{http.MethodPost, "/folders/INBOX/empty", "", http.StatusConflict, "FOLDER_NOT_EMPTIABLE"},
	}
	for _, c := range cases {
		rec := do(env.h, c.method, BasePath+c.path, body(c.body), jsonOrigin, cookie)
		if rec.Code != c.status || errorCode(t, rec) != c.code {
			t.Errorf("%s %s: %d %s", c.method, c.path, rec.Code, rec.Body)
		}
	}

	rec = do(env.h, http.MethodPatch, BasePath+"/folders/Clientes%2F2026", body(`{"name":"Clientes/Antiguos"}`), jsonOrigin, cookie)
	if rec.Code != http.StatusOK || strings.Join(env.mb.renamed, ",") != "Clientes/2026->Clientes/Antiguos" {
		t.Fatalf("renombrar subcarpeta codificada: %d %s %v", rec.Code, rec.Body, env.mb.renamed)
	}
	if rec = do(env.h, http.MethodDelete, BasePath+"/folders/Viejos", nil, jsonOrigin, cookie); rec.Code != http.StatusNoContent {
		t.Fatalf("borrar: %d %s", rec.Code, rec.Body)
	}
	rec = do(env.h, http.MethodPost, BasePath+"/folders/Junk/empty", nil, jsonOrigin, cookie)
	var emptied emptyDTO
	if rec.Code != http.StatusOK {
		t.Fatalf("vaciar: %d %s", rec.Code, rec.Body)
	}
	envelopeData(t, rec, &emptied)
	if emptied.Removed != 3 || strings.Join(env.mb.emptied, ",") != "Junk" {
		t.Fatalf("vaciar: %+v %v", emptied, env.mb.emptied)
	}
}

func TestLotePorHTTP(t *testing.T) {
	env := newTestEnv(t, nopSender{})
	cookie := login(t, env.h)
	path := BasePath + "/folders/INBOX/messages/batch"

	rec := do(env.h, http.MethodPost, path, body(`{"uids":[4,5,4],"action":"move","to":"Viejos"}`), jsonOrigin, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var res batchDTO
	envelopeData(t, rec, &res)
	if res.Affected != 2 || res.Permanent {
		t.Fatalf("%+v", res)
	}
	rec = do(env.h, http.MethodPost, BasePath+"/folders/Trash/messages/batch", body(`{"uids":[7],"action":"delete"}`), jsonOrigin, cookie)
	envelopeData(t, rec, &res)
	if rec.Code != http.StatusOK || !res.Permanent || len(env.mb.expunged) != 1 {
		t.Fatalf("desde la papelera: %d %+v", rec.Code, res)
	}

	uids := make([]string, domain.MaxBatchUIDs+1)
	for i := range uids {
		uids[i] = "1"
	}
	cases := map[string]struct {
		body   string
		status int
		field  string
	}{
		"demasiados": {`{"uids":[` + strings.Join(uids, ",") + `],"action":"delete"}`, http.StatusUnprocessableEntity, "uids"},
		"sin uids":   {`{"uids":[],"action":"delete"}`, http.StatusUnprocessableEntity, "uids"},
		"accion":     {`{"uids":[1],"action":"purge"}`, http.StatusUnprocessableEntity, "action"},
		"flag":       {`{"uids":[1],"action":"flags","add":["\\Deleted"]}`, http.StatusUnprocessableEntity, "add"},
		"negativo":   {`{"uids":[-1],"action":"delete"}`, http.StatusBadRequest, ""},
	}
	for name, c := range cases {
		rec := do(env.h, http.MethodPost, path, body(c.body), jsonOrigin, cookie)
		code, details := errorDetails(t, rec)
		if rec.Code != c.status || (c.field != "" && details["field"] != c.field) {
			t.Errorf("%s: %d %s %v", name, rec.Code, code, details)
		}
	}
}

func TestOriginalPorHTTP(t *testing.T) {
	env := newTestEnv(t, nopSender{})
	cookie := login(t, env.h)
	env.mb.raw = "From: a@b.pe\r\nSubject: hola\r\n\r\n<script>x</script>\r\n"
	rec := do(env.h, http.MethodGet, BasePath+"/folders/INBOX/messages/9/raw", nil, nil, cookie)
	if rec.Code != http.StatusOK || rec.Body.String() != env.mb.raw {
		t.Fatalf("%d %q", rec.Code, rec.Body)
	}
	hd := rec.Header()
	if hd.Get("Content-Type") != "message/rfc822" || !strings.HasPrefix(hd.Get("Content-Disposition"), "attachment") ||
		!strings.Contains(hd.Get("Content-Disposition"), "mensaje-9.eml") || hd.Get("Content-Security-Policy") != partCSP ||
		hd.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("cabeceras: %v", hd)
	}
	env.mb.raw = strings.Repeat("x", 2048)
	if rec := do(env.h, http.MethodGet, BasePath+"/folders/INBOX/messages/9/raw", nil, nil, cookie); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("tope de descarga: %d %s", rec.Code, rec.Body)
	}
	env.mb.raw = ""
	if rec := do(env.h, http.MethodGet, BasePath+"/folders/INBOX/messages/9/raw", nil, nil, cookie); rec.Code != http.StatusNotFound {
		t.Fatalf("inexistente: %d", rec.Code)
	}
}

func TestBusquedaAvanzadaPorHTTP(t *testing.T) {
	env := newTestEnv(t, nopSender{})
	cookie := login(t, env.h)
	env.mb.capped = true
	rec := do(env.h, http.MethodGet, BasePath+"/folders/INBOX/messages?from=ana&to=luis&subject=factura&since=2026-09-01&before=2026-10-01&unread=true&flagged=true&has_attachments=true", nil, nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	f := env.mb.listQuery.Filter
	if f.From != "ana" || f.To != "luis" || f.Subject != "factura" || f.Since.Month() != time.September || f.Before.Month() != time.October ||
		!f.Unread || !f.Flagged || !f.HasAttachments {
		t.Fatalf("filtro: %+v", f)
	}
	var env2 struct {
		Meta struct {
			Total       int  `json:"total"`
			TotalCapped bool `json:"total_capped"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env2); err != nil || !env2.Meta.TotalCapped {
		t.Fatalf("el total acotado se declara: %s", rec.Body)
	}
	for query, field := range map[string]string{
		"since=01-09-2026":                   "since",
		"before=2026-13-01":                  "before",
		"since=2026-10-01&before=2026-09-01": "before",
		"unread=si":                          "unread",
		"has_attachments=1":                  "has_attachments",
		"from=" + strings.Repeat("a", 300):   "from",
		"subject=a%0Db":                      "subject",
	} {
		rec := do(env.h, http.MethodGet, BasePath+"/folders/INBOX/messages?"+query, nil, nil, cookie)
		if _, details := errorDetails(t, rec); rec.Code != http.StatusUnprocessableEntity || details["field"] != field {
			t.Errorf("%s: %d %v", query, rec.Code, details)
		}
	}
}

func multipartForm(t *testing.T, fields map[string]string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf, w.FormDataContentType()
}

func TestEnvioProgramadoPorHTTP(t *testing.T) {
	env := newTestEnv(t, nopSender{})
	cookie := login(t, env.h)
	at := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	form, ct := multipartForm(t, map[string]string{"to": "luis@x.pe", "subject": "Hola", "text": "x", "send_at": at.Format(time.RFC3339)})
	rec := do(env.h, http.MethodPost, BasePath+"/send", form, sendHeaders(ct), cookie)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var res scheduleDTO
	envelopeData(t, rec, &res)
	if res.Scheduled.ID == "" || res.Scheduled.SendAt != at.Format(time.RFC3339) {
		t.Fatalf("%+v", res)
	}
	if len(env.settings.created) != 1 || env.settings.created[0].Username != testUser {
		t.Fatalf("fila: %+v", env.settings.created)
	}

	for name, value := range map[string]string{"ilegible": "manana", "pasado": time.Now().Add(-time.Hour).Format(time.RFC3339), "lejos": time.Now().AddDate(0, 0, 40).Format(time.RFC3339)} {
		form, ct := multipartForm(t, map[string]string{"to": "luis@x.pe", "text": "x", "send_at": value})
		rec := do(env.h, http.MethodPost, BasePath+"/send", form, sendHeaders(ct), cookie)
		if _, details := errorDetails(t, rec); rec.Code != http.StatusUnprocessableEntity || details["field"] != "send_at" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	form, ct = multipartForm(t, map[string]string{"text": "x", "send_at": at.Format(time.RFC3339)})
	rec = do(env.h, http.MethodPost, BasePath+"/drafts", form, map[string]string{"Origin": allowedOrigin, "Content-Type": ct}, cookie)
	if _, details := errorDetails(t, rec); rec.Code != http.StatusUnprocessableEntity || details["field"] != "send_at" {
		t.Errorf("un borrador no se programa: %d %s", rec.Code, rec.Body)
	}

	rec = do(env.h, http.MethodGet, BasePath+"/scheduled", nil, nil, cookie)
	var rows []scheduledDTO
	envelopeData(t, rec, &rows)
	if rec.Code != http.StatusOK || len(rows) != 1 || rows[0].ID != res.Scheduled.ID || rows[0].Status != "pending" || rows[0].Subject != "Hola" ||
		strings.Join(rows[0].Recipients, ",") != "luis@x.pe" {
		t.Fatalf("listado: %d %+v", rec.Code, rows)
	}

	later := at.Add(time.Hour).Format(time.RFC3339)
	rec = do(env.h, http.MethodPatch, BasePath+"/scheduled/"+res.Scheduled.ID, body(`{"send_at":"`+later+`"}`), jsonOrigin, cookie)
	var row scheduledDTO
	envelopeData(t, rec, &row)
	if rec.Code != http.StatusOK || row.SendAt != later {
		t.Fatalf("reprogramar: %d %+v", rec.Code, row)
	}
	if rec = do(env.h, http.MethodDelete, BasePath+"/scheduled/"+res.Scheduled.ID, nil, jsonOrigin, cookie); rec.Code != http.StatusNoContent {
		t.Fatalf("cancelar: %d %s", rec.Code, rec.Body)
	}
	rec = do(env.h, http.MethodDelete, BasePath+"/scheduled/00000000-0000-4000-8000-000000000009", nil, jsonOrigin, cookie)
	if rec.Code != http.StatusNotFound || errorCode(t, rec) != "SCHEDULED_SEND_NOT_FOUND" {
		t.Fatalf("inexistente: %d %s", rec.Code, rec.Body)
	}
	rec = do(env.h, http.MethodDelete, BasePath+"/scheduled/..%2Fx", nil, jsonOrigin, cookie)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("id invalido: %d %s", rec.Code, rec.Body)
	}
}

func TestFirmaYReglasPorHTTP(t *testing.T) {
	env := newTestEnv(t, nopSender{})
	cookie := login(t, env.h)

	rec := do(env.h, http.MethodGet, BasePath+"/signature?username=otro@empresa.pe", nil, nil, cookie)
	var sig signatureDTO
	envelopeData(t, rec, &sig)
	if rec.Code != http.StatusOK || env.settings.username != testUser || sig.Limits.MaxHTMLBytes != 8192 || sig.Limits.MaxTextBytes != 4096 {
		t.Fatalf("firma: %d %+v %q", rec.Code, sig, env.settings.username)
	}
	rec = do(env.h, http.MethodPut, BasePath+"/signature", body(`{"enabled":true,"html":"<b>Ana</b>","on_replies":true}`), jsonOrigin, cookie)
	if rec.Code != http.StatusOK || env.settings.signatureIn.HTML != "<b>Ana</b>" || env.settings.signatureIn.Text != "<b>Ana</b>" || !env.settings.signatureIn.OnReplies {
		t.Fatalf("guardar firma: %d %+v", rec.Code, env.settings.signatureIn)
	}
	if rec = do(env.h, http.MethodPut, BasePath+"/signature", body(`{"enabled":true,"html":"x","text":"otro"}`), jsonOrigin, cookie); rec.Code != http.StatusBadRequest {
		t.Fatalf("el texto lo genera el servicio: %d", rec.Code)
	}

	rules := `{"rules":[{"id":"","name":"Facturas","enabled":true,"match":"any","conditions":[{"field":"from","op":"contains","value":"x"}],` +
		`"actions":[{"type":"move","folder":"Facturas"},{"type":"forward","address":"c@x.pe","keep_copy":true}],"stop":false}],` +
		`"forwarding":{"enabled":true,"addresses":["yo@x.pe"],"keep_copy":true}}`
	rec = do(env.h, http.MethodPut, BasePath+"/filters", body(rules), jsonOrigin, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("reglas: %d %s", rec.Code, rec.Body)
	}
	var got filtersDTO
	envelopeData(t, rec, &got)
	if len(got.Rules) != 1 || *got.Rules[0].Actions[1].KeepCopy != true || got.Rules[0].Actions[0].Address != "" || got.Limits["max_rules"] != 50 {
		t.Fatalf("%+v", got)
	}
	env.settings.err = domain.NewValidationError("rules[0].conditions[0].value", "demasiado largo")
	rec = do(env.h, http.MethodPut, BasePath+"/filters", body(rules), jsonOrigin, cookie)
	if code, details := errorDetails(t, rec); rec.Code != http.StatusUnprocessableEntity || code != "VALIDATION_ERROR" || details["field"] != "rules[0].conditions[0].value" {
		t.Fatalf("rechazo del directorio: %d %s", rec.Code, rec.Body)
	}
}

func TestCambioDeContrasenaPorHTTP(t *testing.T) {
	env := newTestEnv(t, nopSender{})
	cookie := login(t, env.h)
	rec := do(env.h, http.MethodPost, BasePath+"/password", body(`{"current_password":"mala","new_password":"nueva-segura-123"}`), jsonOrigin, cookie)
	if rec.Code != http.StatusUnauthorized || errorCode(t, rec) != "INVALID_CREDENTIALS" || sessionCookie(rec) != nil {
		t.Fatalf("actual mala: %d %s", rec.Code, rec.Body)
	}
	if env.settings.password != "" {
		t.Fatal("no se cambia nada")
	}
	if rec := do(env.h, http.MethodGet, BasePath+"/session", nil, nil, cookie); rec.Code != http.StatusOK {
		t.Fatalf("la sesion sigue abierta: %d", rec.Code)
	}
	rec = do(env.h, http.MethodPost, BasePath+"/password", body(`{"current_password":"`+testPass+`","new_password":"nueva-segura-123"}`), jsonOrigin, cookie)
	if rec.Code != http.StatusNoContent || env.settings.password != "nueva-segura-123" || env.settings.username != testUser {
		t.Fatalf("cambio: %d %s", rec.Code, rec.Body)
	}
	if c := sessionCookie(rec); c == nil || c.MaxAge >= 0 {
		t.Fatalf("la cookie se borra: %+v", c)
	}
}

func TestLibretaPorHTTP(t *testing.T) {
	env := newTestEnv(t, nopSender{})
	cookie := login(t, env.h)

	rec := do(env.h, http.MethodGet, BasePath+"/contacts?q=luis&page=2&per_page=25", nil, nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var list []contactDTO
	envelopeData(t, rec, &list)
	if len(list) != 1 || list[0].ID != "c1" || list[0].Emails[0].Value != "luis@x.pe" || list[0].Phones == nil {
		t.Fatalf("%+v", list)
	}
	if env.dav.mb.TenantID != testTenant || env.dav.mb.MailboxID != testMailbox || env.dav.query.Search != "luis" || env.dav.query.Page != 2 {
		t.Fatalf("identidad y busqueda: %+v %+v", env.dav.mb, env.dav.query)
	}
	var page struct {
		Meta struct {
			Total int `json:"total"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || page.Meta.Total != 1 {
		t.Fatalf("meta: %s", rec.Body)
	}

	headers := map[string]string{"Origin": allowedOrigin, "Content-Type": "application/json", "If-Match": `"v1"`}
	rec = do(env.h, http.MethodPut, BasePath+"/contacts/c1", body(`{"name":"Luis","emails":[{"value":"l@x.pe","type":"home"}]}`), headers, cookie)
	if rec.Code != http.StatusOK || env.dav.ifMatch != `"v1"` || env.dav.contact.Emails[0].Type != "home" {
		t.Fatalf("editar: %d %s %q", rec.Code, rec.Body, env.dav.ifMatch)
	}
	if rec.Header().Get("ETag") != `"v2"` {
		t.Fatalf("la version nueva viaja en ETag: %v", rec.Header())
	}
	env.dav.err = &domain.ServiceRejection{Kind: domain.RejectPrecondition, Code: "PRECONDITION_FAILED", Message: "cambio", ETag: `"v7"`}
	rec = do(env.h, http.MethodPut, BasePath+"/contacts/c1", body(`{}`), headers, cookie)
	if rec.Code != http.StatusPreconditionFailed || errorCode(t, rec) != "PRECONDITION_FAILED" || rec.Header().Get("ETag") != `"v7"` {
		t.Fatalf("412: %d %s %v", rec.Code, rec.Body, rec.Header())
	}
	env.dav.err = &domain.ServiceRejection{Kind: domain.RejectNotFound, Code: "NOT_FOUND", Message: "no existe"}
	if rec = do(env.h, http.MethodGet, BasePath+"/contacts/c9", nil, nil, cookie); rec.Code != http.StatusNotFound || errorCode(t, rec) != "NOT_FOUND" {
		t.Fatalf("404: %d %s", rec.Code, rec.Body)
	}
	env.dav.err = &domain.ServiceRejection{Kind: domain.RejectValidation, Code: "VALIDATION_ERROR", Message: "invalido", Details: map[string]string{"field": "emails[1].value"}}
	rec = do(env.h, http.MethodPost, BasePath+"/contacts", body(`{"name":"x"}`), jsonOrigin, cookie)
	if code, details := errorDetails(t, rec); rec.Code != http.StatusUnprocessableEntity || code != "VALIDATION_ERROR" || details["field"] != "emails[1].value" {
		t.Fatalf("422: %d %s", rec.Code, rec.Body)
	}
	env.dav.err = nil
	if rec = do(env.h, http.MethodPost, BasePath+"/contacts", body(`{"name":"Nuevo","unknown":1}`), jsonOrigin, cookie); rec.Code != http.StatusBadRequest {
		t.Fatalf("campo desconocido: %d", rec.Code)
	}
	if rec = do(env.h, http.MethodPost, BasePath+"/contacts", body(`{"name":"Nuevo"}`), jsonOrigin, cookie); rec.Code != http.StatusCreated {
		t.Fatalf("crear: %d %s", rec.Code, rec.Body)
	}
	if rec = do(env.h, http.MethodDelete, BasePath+"/contacts/c1", nil, jsonOrigin, cookie); rec.Code != http.StatusNoContent || env.dav.id != "c1" {
		t.Fatalf("borrar: %d", rec.Code)
	}

	rec = do(env.h, http.MethodGet, BasePath+"/contacts/export", nil, nil, cookie)
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/vcard") ||
		!strings.Contains(rec.Header().Get("Content-Disposition"), "attachment") || !strings.Contains(rec.Body.String(), "BEGIN:VCARD") {
		t.Fatalf("exportar: %d %v %q", rec.Code, rec.Header(), rec.Body)
	}

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, _ := w.CreateFormFile("file", "agenda.vcf")
	_, _ = fw.Write([]byte("BEGIN:VCARD\r\nFN:Luis\r\nEND:VCARD\r\n"))
	_ = w.Close()
	rec = do(env.h, http.MethodPost, BasePath+"/contacts/import", &buf, map[string]string{"Origin": allowedOrigin, "Content-Type": w.FormDataContentType()}, cookie)
	var imp importDTO
	if rec.Code != http.StatusOK {
		t.Fatalf("importar: %d %s", rec.Code, rec.Body)
	}
	envelopeData(t, rec, &imp)
	if imp.Imported != 2 || imp.Updated != 1 || imp.Skipped[0].Index != 3 || env.dav.filename != "agenda.vcf" || !strings.Contains(string(env.dav.imported), "FN:Luis") {
		t.Fatalf("%+v %q", imp, env.dav.filename)
	}

	buf.Reset()
	w = multipart.NewWriter(&buf)
	fw, _ = w.CreateFormFile("file", "grande.vcf")
	_, _ = fw.Write(bytes.Repeat([]byte("x"), 600))
	_ = w.Close()
	rec = do(env.h, http.MethodPost, BasePath+"/contacts/import", &buf, map[string]string{"Origin": allowedOrigin, "Content-Type": w.FormDataContentType()}, cookie)
	if rec.Code != http.StatusRequestEntityTooLarge || errorCode(t, rec) != "IMPORT_TOO_LARGE" {
		t.Fatalf("tope: %d %s", rec.Code, rec.Body)
	}
}

func TestCalendarioPorHTTP(t *testing.T) {
	env := newTestEnv(t, nopSender{})
	cookie := login(t, env.h)
	rec := do(env.h, http.MethodGet, BasePath+"/calendar/events?start=2026-09-01T00:00:00Z&end=2026-10-01T00:00:00Z", nil, nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var occ []occurrenceDTO
	envelopeData(t, rec, &occ)
	if len(occ) != 1 || !occ[0].Recurring || env.dav.window.Start.Day() != 1 {
		t.Fatalf("%+v", occ)
	}
	if rec = do(env.h, http.MethodGet, BasePath+"/calendar/events?start=ayer&end=hoy", nil, nil, cookie); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("ventana ilegible: %d", rec.Code)
	}
	event := `{"title":"Revision","start":"2026-09-25T10:00:00Z","end":"2026-09-25T11:00:00Z","all_day":false,"location":"","description":"",` +
		`"recurrence":{"freq":"weekly","interval":1,"count":null,"until":null,"by_day":["MO"]},"reminder_minutes":15}`
	rec = do(env.h, http.MethodPost, BasePath+"/calendar/events", body(event), jsonOrigin, cookie)
	var got savedEventDTO
	if rec.Code != http.StatusCreated {
		t.Fatalf("crear: %d %s", rec.Code, rec.Body)
	}
	envelopeData(t, rec, &got)
	if got.ID != "e9" || got.Recurrence == nil || got.Recurrence.Freq != "weekly" || *got.ReminderMinutes != 15 || env.dav.event.Recurrence.ByDay[0] != "MO" {
		t.Fatalf("%+v", got)
	}
	headers := map[string]string{"Origin": allowedOrigin, "Content-Type": "application/json", "If-Match": `"e1"`}
	if rec = do(env.h, http.MethodPut, BasePath+"/calendar/events/e1", body(`{"title":"Otra","recurrence":null}`), headers, cookie); rec.Code != http.StatusOK || env.dav.ifMatch != `"e1"` {
		t.Fatalf("editar: %d %s", rec.Code, rec.Body)
	}
	env.dav.err = &domain.ServiceRejection{Kind: domain.RejectValidation, Code: "VALIDATION_ERROR", Message: "ventana excesiva",
		Details: map[string]string{"field": "end", "max_days": "62"}}
	rec = do(env.h, http.MethodGet, BasePath+"/calendar/events?start=2026-01-01T00:00:00Z&end=2026-12-01T00:00:00Z", nil, nil, cookie)
	if _, details := errorDetails(t, rec); rec.Code != http.StatusUnprocessableEntity || details["max_days"] != "62" {
		t.Fatalf("ventana: %d %s", rec.Code, rec.Body)
	}
	env.dav.err = &domain.ServiceRejection{Kind: domain.RejectRateLimited, Code: "RATE_LIMITED", Message: "espera", RetryAfter: "3"}
	if rec = do(env.h, http.MethodDelete, BasePath+"/calendar/events/e1", nil, jsonOrigin, cookie); rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "3" {
		t.Fatalf("cupo: %d %s", rec.Code, rec.Body)
	}
	env.dav.err = nil
	rec = do(env.h, http.MethodGet, BasePath+"/meta/dav", nil, nil, cookie)
	var meta davMetaDTO
	envelopeData(t, rec, &meta)
	if rec.Code != http.StatusOK || meta.Limits["max_event_window_days"] != 62 {
		t.Fatalf("topes de mail-dav: %d %+v", rec.Code, meta)
	}
}

// Una sesion abierta antes de que mail-auth devolviera la empresa y el buzon no llega a mail-dav:
// responde SESSION_EXPIRED, borra la cookie y la sesion deja de valer.
func TestSesionAnteriorSinEmpresaEnLibretaYCalendario(t *testing.T) {
	env := newTestEnv(t, nopSender{})
	rec := do(env.h, http.MethodPost, BasePath+"/session", loginBody(legacyUser, testPass), map[string]string{"Origin": allowedOrigin}, nil)
	cookie := sessionCookie(rec)
	if rec.Code != http.StatusOK || cookie == nil {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	rec = do(env.h, http.MethodGet, BasePath+"/contacts", nil, nil, cookie)
	if rec.Code != http.StatusUnauthorized || errorCode(t, rec) != "SESSION_EXPIRED" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if c := sessionCookie(rec); c == nil || c.MaxAge >= 0 {
		t.Fatalf("la cookie se borra: %+v", c)
	}
	if rec := do(env.h, http.MethodGet, BasePath+"/session", nil, nil, cookie); rec.Code != http.StatusUnauthorized {
		t.Fatalf("la sesion se cierra: %d", rec.Code)
	}
	if env.dav.calls != 0 {
		t.Fatal("mail-dav no recibe nada")
	}
}

func TestCodigosDeLosErroresNuevos(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{domain.ErrFolderProtected, http.StatusConflict, "FOLDER_PROTECTED"},
		{domain.ErrFolderHasChildren, http.StatusConflict, "FOLDER_HAS_CHILDREN"},
		{domain.ErrFolderExists, http.StatusConflict, "FOLDER_EXISTS"},
		{domain.ErrFolderNotEmptiable, http.StatusConflict, "FOLDER_NOT_EMPTIABLE"},
		{domain.ErrScheduledNotFound, http.StatusNotFound, "SCHEDULED_SEND_NOT_FOUND"},
		{domain.ErrScheduledNotPending, http.StatusConflict, "SCHEDULED_SEND_NOT_PENDING"},
		{domain.ErrScheduledNotClaimed, http.StatusConflict, "SCHEDULED_SEND_NOT_CLAIMED"},
		{domain.ErrScheduledLimit, http.StatusConflict, "SCHEDULED_SEND_LIMIT"},
		{&domain.ServiceRejection{Kind: domain.RejectQuota, Code: "LIMIT_EXCEEDED"}, http.StatusInsufficientStorage, "LIMIT_EXCEEDED"},
		{&domain.ServiceRejection{Kind: domain.RejectTooLarge, Code: "PAYLOAD_TOO_LARGE"}, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE"},
		{&domain.ServiceRejection{Kind: domain.RejectBadRequest, Code: "BAD_REQUEST"}, http.StatusBadRequest, "BAD_REQUEST"},
		{&domain.ServiceRejection{Kind: domain.RejectUnavailable, Code: "SERVICE_UNAVAILABLE"}, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE"},
		{domain.ErrImportTooLarge, http.StatusRequestEntityTooLarge, "IMPORT_TOO_LARGE"},
		{errors.Join(domain.ErrUnavailable, errors.New("x")), http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE"},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		writeError(rec, c.err)
		if rec.Code != c.status || errorCode(t, rec) != c.code {
			t.Errorf("%v: %d %s", c.err, rec.Code, rec.Body)
		}
	}
}
