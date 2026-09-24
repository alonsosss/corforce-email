package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

var idemSeq int

// sendHeaders son las cabeceras de un envio desde la aplicacion, con una clave nueva.
func sendHeaders(contentType string) map[string]string {
	idemSeq++
	return map[string]string{
		"Origin": allowedOrigin, "Content-Type": contentType, idempotencyHeader: fmt.Sprintf("clave-http-%08d", idemSeq),
	}
}

func envelopeData(t *testing.T, rec *httptest.ResponseRecorder, dst any) {
	t.Helper()
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("cuerpo no es JSON: %q", rec.Body.String())
	}
	dec := json.NewDecoder(bytes.NewReader(env.Data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		t.Fatalf("el cuerpo no cumple el contrato: %v (%s)", err, env.Data)
	}
}

func errorDetails(t *testing.T, rec *httptest.ResponseRecorder) (string, map[string]string) {
	t.Helper()
	var env struct {
		Error struct {
			Code    string            `json:"code"`
			Details map[string]string `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("cuerpo no es JSON: %q", rec.Body.String())
	}
	return env.Error.Code, env.Error.Details
}

// metaContract escribe a mano los nombres JSON que consume la interfaz (web/src/api/webmail.ts):
// renombrar un campo en el servidor rompe esta prueba antes que la pantalla.
type metaContract struct {
	Limits struct {
		MaxRecipients      int   `json:"max_recipients"`
		MaxMessageBytes    int64 `json:"max_message_bytes"`
		MaxAttachments     int   `json:"max_attachments"`
		MaxDownloadBytes   int64 `json:"max_download_bytes"`
		MaxBodyPartBytes   int64 `json:"max_body_part_bytes"`
		MaxSubjectChars    int   `json:"max_subject_chars"`
		MaxSearchBytes     int   `json:"max_search_bytes"`
		MaxFolderNameBytes int   `json:"max_folder_name_bytes"`
		MaxBatchUIDs       int   `json:"max_batch_uids"`
		MaxScheduledDays   int   `json:"max_scheduled_days"`
		MaxReminderDays    int   `json:"max_reminder_days"`
		MaxImportBytes     int64 `json:"max_import_bytes"`
	} `json:"limits"`
	Pagination struct {
		DefaultPageSize int `json:"default_page_size"`
		MaxPageSize     int `json:"max_page_size"`
	} `json:"pagination"`
	FolderRoles  []string `json:"folder_roles"`
	MutableFlags []string `json:"mutable_flags"`
	Session      struct {
		IdleTimeoutSeconds int64 `json:"idle_timeout_seconds"`
		MaxLifetimeSeconds int64 `json:"max_lifetime_seconds"`
	} `json:"session"`
}

func TestMetaSirveLosTopesQueAplicaElServicio(t *testing.T) {
	h, _ := newTestHandler(t)
	if rec := do(h, http.MethodGet, BasePath+"/meta", nil, nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("la meta exige sesion: %d", rec.Code)
	}
	cookie := login(t, h)
	rec := do(h, http.MethodGet, BasePath+"/meta", nil, nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got metaContract
	envelopeData(t, rec, &got)
	roles := make([]string, len(domain.SpecialRoles))
	for i, r := range domain.SpecialRoles {
		roles[i] = string(r)
	}
	flags := make([]string, len(domain.MutableFlags))
	for i, f := range domain.MutableFlags {
		flags[i] = string(f)
	}
	checks := []struct {
		name      string
		got, want any
	}{
		{"max_recipients", got.Limits.MaxRecipients, 2},
		{"max_message_bytes", got.Limits.MaxMessageBytes, int64(4096)},
		{"max_attachments", got.Limits.MaxAttachments, domain.MaxAttachments},
		{"max_download_bytes", got.Limits.MaxDownloadBytes, int64(1024)},
		{"max_body_part_bytes", got.Limits.MaxBodyPartBytes, int64(1024)},
		{"max_subject_chars", got.Limits.MaxSubjectChars, domain.MaxSubjectRunes},
		{"max_search_bytes", got.Limits.MaxSearchBytes, domain.MaxSearchBytes},
		{"max_folder_name_bytes", got.Limits.MaxFolderNameBytes, domain.MaxFolderNameBytes},
		{"max_batch_uids", got.Limits.MaxBatchUIDs, domain.MaxBatchUIDs},
		{"max_scheduled_days", got.Limits.MaxScheduledDays, 30},
		{"max_reminder_days", got.Limits.MaxReminderDays, 30},
		{"max_import_bytes", got.Limits.MaxImportBytes, int64(512)},
		{"default_page_size", got.Pagination.DefaultPageSize, domain.DefaultPerPage},
		{"max_page_size", got.Pagination.MaxPageSize, domain.MaxPerPage},
		{"folder_roles", got.FolderRoles, roles},
		{"mutable_flags", got.MutableFlags, flags},
		{"idle_timeout_seconds", got.Session.IdleTimeoutSeconds, int64(1800)},
		{"max_lifetime_seconds", got.Session.MaxLifetimeSeconds, int64(12 * 3600)},
	}
	for _, c := range checks {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s = %v, quiero %v", c.name, c.got, c.want)
		}
	}
}

func TestIdentitiesDelBuzon(t *testing.T) {
	h, _ := newTestHandler(t)
	cookie := login(t, h)
	rec := do(h, http.MethodGet, BasePath+"/identities", nil, nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got []struct {
		Email   string `json:"email"`
		Name    string `json:"name"`
		Primary bool   `json:"primary"`
	}
	envelopeData(t, rec, &got)
	if len(got) != 2 || got[0].Email != testUser || got[0].Name != "Ana" || !got[0].Primary ||
		got[1].Email != "ventas@empresa.pe" || got[1].Primary {
		t.Fatalf("remitentes: %+v", got)
	}
}

type countingSender struct {
	calls int
	err   error
}

func (s *countingSender) Send(context.Context, string, string, []string, []byte) error {
	s.calls++
	return s.err
}

func TestEnvioExigeClaveYNoSeRepite(t *testing.T) {
	sender := &countingSender{}
	h, _ := newTestHandlerWith(t, sender)
	cookie := login(t, h)
	fields := map[string]string{"to": "a@x.com", "subject": "Hola", "text": "cuerpo"}

	body, ctype := multipartBody(t, fields, 0)
	rec := do(h, http.MethodPost, BasePath+"/send", body, map[string]string{"Origin": allowedOrigin, "Content-Type": ctype}, cookie)
	if code, details := errorDetails(t, rec); rec.Code != http.StatusUnprocessableEntity || code != "VALIDATION_ERROR" || details["field"] != "idempotency_key" {
		t.Fatalf("sin clave: %d %s", rec.Code, rec.Body.String())
	}

	headers := sendHeaders(ctype)
	var results []sendDTO
	for i := 0; i < 2; i++ {
		body, ctype := multipartBody(t, fields, 0)
		headers["Content-Type"] = ctype
		rec := do(h, http.MethodPost, BasePath+"/send", body, headers, cookie)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("envio %d: %d %s", i, rec.Code, rec.Body.String())
		}
		var res sendDTO
		envelopeData(t, rec, &res)
		results = append(results, res)
	}
	if sender.calls != 1 || results[0].Replayed || !results[1].Replayed || results[0].MessageID != results[1].MessageID {
		t.Fatalf("envios=%d resultados=%+v", sender.calls, results)
	}
}

func TestDestinatarioRechazadoLlevaLaDireccion(t *testing.T) {
	sender := &countingSender{err: fmt.Errorf("550 5.1.1 unknown: %w", &domain.RecipientRejectedError{Address: "nadie@empresa.pe"})}
	h, _ := newTestHandlerWith(t, sender)
	cookie := login(t, h)
	body, ctype := multipartBody(t, map[string]string{"to": "nadie@empresa.pe", "text": "x"}, 0)
	rec := do(h, http.MethodPost, BasePath+"/send", body, sendHeaders(ctype), cookie)
	if code, details := errorDetails(t, rec); rec.Code != http.StatusUnprocessableEntity || code != "RECIPIENT_REJECTED" || details["address"] != "nadie@empresa.pe" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestEnvioRetiraElBorradorYAdjuntaDelServidor(t *testing.T) {
	h, mb := newTestHandler(t)
	cookie := login(t, h)
	mb.part, mb.body = domain.Part{ID: "2", Filename: "informe.pdf", ContentType: "application/pdf"}, "%PDF"
	body, ctype := multipartBody(t, map[string]string{
		"to": "a@x.com", "text": "reenvio", "replace_uid": "5",
		"source_folder": "INBOX", "source_uid": "9", "source_parts": "2",
	}, 0)
	rec := do(h, http.MethodPost, BasePath+"/send", body, sendHeaders(ctype), cookie)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var res sendDTO
	envelopeData(t, rec, &res)
	if !res.SavedToSent || !res.DraftRemoved || len(mb.expunged) != 1 || mb.expunged[0] != 5 || len(mb.partReqs) != 1 || mb.partReqs[0] != "2" {
		t.Fatalf("resultado=%+v expunged=%v partes=%v", res, mb.expunged, mb.partReqs)
	}

	for field, value := range map[string]string{"source_parts": "1..2", "source_uid": "0", "source_folder": "INBOX\r\nA1 DELETE"} {
		fields := map[string]string{"to": "a@x.com", "source_folder": "INBOX", "source_uid": "9", "source_parts": "2"}
		fields[field] = value
		body, ctype := multipartBody(t, fields, 0)
		rec := do(h, http.MethodPost, BasePath+"/send", body, sendHeaders(ctype), cookie)
		if code, details := errorDetails(t, rec); rec.Code != http.StatusUnprocessableEntity || code != "VALIDATION_ERROR" || details["field"] != field {
			t.Errorf("%s=%q: %d %s", field, value, rec.Code, rec.Body.String())
		}
	}
}
