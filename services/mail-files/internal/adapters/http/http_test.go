package http

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-files/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type fakeUC struct {
	enabled     bool
	policy      domain.Policy
	uploadErr   error
	uploaded    struct{ name, body string }
	opts        domain.UploadOptions
	owner       domain.Owner
	listing     domain.Listing
	revokeErr   error
	inspectErr  error
	downloadErr error
	file        domain.File
	content     string
	claims      domain.LinkClaims
	sig         string
}

func (f *fakeUC) Enabled() bool         { return f.enabled }
func (f *fakeUC) Policy() domain.Policy { return f.policy }

func (f *fakeUC) Upload(_ context.Context, o domain.Owner, name string, body io.Reader, opts domain.UploadOptions) (domain.SharedFile, error) {
	if f.uploadErr != nil {
		return domain.SharedFile{}, f.uploadErr
	}
	b, _ := io.ReadAll(body)
	f.uploaded.name, f.uploaded.body, f.opts, f.owner = name, string(b), opts, o
	return domain.SharedFile{File: f.file, URL: "https://correo.example.com/enlace"}, nil
}

func (f *fakeUC) List(_ context.Context, o domain.Owner) (domain.Listing, error) {
	f.owner = o
	return f.listing, nil
}

func (f *fakeUC) Revoke(_ context.Context, o domain.Owner, _ uuid.UUID) (domain.SharedFile, error) {
	f.owner = o
	if f.revokeErr != nil {
		return domain.SharedFile{}, f.revokeErr
	}
	revoked := f.file
	revoked.Status = domain.StatusRevoked
	return domain.SharedFile{File: revoked, URL: "https://correo.example.com/enlace"}, nil
}

func (f *fakeUC) Inspect(_ context.Context, c domain.LinkClaims, sig string) (domain.File, error) {
	f.claims, f.sig = c, sig
	return f.file, f.inspectErr
}

func (f *fakeUC) Download(_ context.Context, c domain.LinkClaims, sig string) (domain.File, io.ReadCloser, error) {
	f.claims, f.sig = c, sig
	if f.downloadErr != nil {
		return domain.File{}, nil, f.downloadErr
	}
	return f.file, io.NopCloser(strings.NewReader(f.content)), nil
}

func testPolicy() domain.Policy {
	return domain.Policy{MaxFileBytes: 1000, DefaultExpiryDays: 7, MaxExpiryDays: 30, DefaultMaxDownloads: 5, MaxDownloads: 10,
		MailboxQuotaBytes: 5000, TenantQuotaBytes: 9000, MaxActivePerMailbox: 20}
}

func newTestHandler(t *testing.T, uc *fakeUC) *Handler {
	t.Helper()
	h, err := NewHandler(uc, Config{UploadTimeout: time.Minute, DownloadTimeout: time.Minute, OperationTimeout: time.Second}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func activeFile(name string) domain.File {
	return domain.File{
		ID: uuid.New(), TenantID: uuid.New(), MailboxID: uuid.New(), Name: name, SizeBytes: 7,
		SHA256: strings.Repeat("ab", 32), Status: domain.StatusReady, ExpiresAt: time.Now().Add(time.Hour),
		MaxDownloads: 3, Downloads: 1, CreatedAt: time.Now(),
	}
}

type envelope struct {
	Data  json.RawMessage `json:"data"`
	Error *struct {
		Code    string            `json:"code"`
		Details map[string]string `json:"details"`
	} `json:"error"`
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) envelope {
	t.Helper()
	var env envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("respuesta ilegible (%d): %s", rec.Code, rec.Body.String())
	}
	return env
}

func internalRequest(method, target string, body io.Reader, owner domain.Owner) *http.Request {
	r := httptest.NewRequest(method, target, body)
	r.Header.Set(TenantHeader, owner.TenantID.String())
	r.Header.Set(MailboxHeader, owner.MailboxID.String())
	return r
}

func TestSubidaInternaLeeNombreOpcionesYBuzon(t *testing.T) {
	uc := &fakeUC{enabled: true, policy: testPolicy(), file: activeFile("a.pdf")}
	h := newTestHandler(t, uc)
	owner := domain.Owner{TenantID: uuid.New(), MailboxID: uuid.New()}
	rec := httptest.NewRecorder()
	h.InternalRoutes().ServeHTTP(rec, internalRequest(http.MethodPost,
		InternalPrefix+"/files?name=contrato%20final.pdf&expires_in_days=3&max_downloads=4", strings.NewReader("contenido"), owner))
	if rec.Code != http.StatusCreated {
		t.Fatalf("estado %d: %s", rec.Code, rec.Body.String())
	}
	if uc.uploaded.name != "contrato final.pdf" || uc.uploaded.body != "contenido" || uc.opts != (domain.UploadOptions{ExpiresInDays: 3, MaxDownloads: 4}) || uc.owner != owner {
		t.Fatalf("subida: %+v %+v %+v", uc.uploaded, uc.opts, uc.owner)
	}
	var out fileDTO
	_ = json.Unmarshal(decode(t, rec).Data, &out)
	if out.URL == "" || out.State != "active" || out.RemainingDownloads != 2 {
		t.Fatalf("dto: %+v", out)
	}
}

func TestLaAPIInternaExigeEmpresaYBuzon(t *testing.T) {
	h := newTestHandler(t, &fakeUC{policy: testPolicy()})
	for _, hdr := range []map[string]string{{}, {TenantHeader: "x", MailboxHeader: uuid.NewString()}, {TenantHeader: uuid.Nil.String(), MailboxHeader: uuid.NewString()}} {
		r := httptest.NewRequest(http.MethodGet, InternalPrefix+"/files", nil)
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.InternalRoutes().ServeHTTP(rec, r)
		if rec.Code != http.StatusBadRequest || decode(t, rec).Error.Code != "MAILBOX_REQUIRED" {
			t.Fatalf("%v: %d %s", hdr, rec.Code, rec.Body.String())
		}
	}
}

func TestErroresDeSubidaConSusCodigos(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{domain.ErrInfected, http.StatusUnprocessableEntity, "FILE_INFECTED"},
		{domain.ErrScanUnavailable, http.StatusServiceUnavailable, "SCAN_UNAVAILABLE"},
		{domain.ErrTooLarge, http.StatusRequestEntityTooLarge, "FILE_TOO_LARGE"},
		{domain.ErrEmpty, http.StatusUnprocessableEntity, "FILE_EMPTY"},
		{domain.ErrIncomplete, http.StatusBadRequest, "UPLOAD_INCOMPLETE"},
		{domain.ErrMailboxQuota, http.StatusInsufficientStorage, "MAILBOX_FILES_QUOTA_EXCEEDED"},
		{domain.ErrTenantQuota, http.StatusInsufficientStorage, "TENANT_FILES_QUOTA_EXCEEDED"},
		{domain.ErrTooManyFiles, http.StatusInsufficientStorage, "SHARED_FILES_LIMIT"},
		{domain.ErrBusy, http.StatusTooManyRequests, "UPLOADS_BUSY"},
		{domain.ErrStorageDisabled, http.StatusServiceUnavailable, "LARGE_FILES_DISABLED"},
		{domain.ErrUnavailable, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE"},
		{domain.NewValidationError("name", "mal"), http.StatusUnprocessableEntity, "VALIDATION_ERROR"},
	}
	for _, c := range cases {
		h := newTestHandler(t, &fakeUC{policy: testPolicy(), uploadErr: c.err})
		rec := httptest.NewRecorder()
		h.InternalRoutes().ServeHTTP(rec, internalRequest(http.MethodPost, InternalPrefix+"/files?name=a", strings.NewReader("x"),
			domain.Owner{TenantID: uuid.New(), MailboxID: uuid.New()}))
		env := decode(t, rec)
		if rec.Code != c.status || env.Error == nil || env.Error.Code != c.code {
			t.Errorf("%v: %d %s", c.err, rec.Code, rec.Body.String())
		}
		if c.err == domain.ErrBusy && rec.Header().Get("Retry-After") == "" {
			t.Error("UPLOADS_BUSY sin Retry-After")
		}
	}
}

func TestSubidaDeclaradaMayorQueElTopeNoSeLee(t *testing.T) {
	uc := &fakeUC{policy: testPolicy()}
	h := newTestHandler(t, uc)
	r := internalRequest(http.MethodPost, InternalPrefix+"/files?name=a", strings.NewReader(strings.Repeat("x", 1001)),
		domain.Owner{TenantID: uuid.New(), MailboxID: uuid.New()})
	rec := httptest.NewRecorder()
	h.InternalRoutes().ServeHTTP(rec, r)
	if rec.Code != http.StatusRequestEntityTooLarge || uc.uploaded.body != "" || decode(t, rec).Error.Details["limit"] != "1000" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	for _, q := range []string{"expires_in_days=0", "expires_in_days=x", "max_downloads=-2"} {
		rec := httptest.NewRecorder()
		h.InternalRoutes().ServeHTTP(rec, internalRequest(http.MethodPost, InternalPrefix+"/files?name=a&"+q, strings.NewReader("x"),
			domain.Owner{TenantID: uuid.New(), MailboxID: uuid.New()}))
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d", q, rec.Code)
		}
	}
}

func TestListadoYMetaSirvenLaPolitica(t *testing.T) {
	active := activeFile("a.pdf")
	revoked := activeFile("b.pdf")
	revoked.Status = domain.StatusRevoked
	uc := &fakeUC{enabled: true, policy: testPolicy(), listing: domain.Listing{
		Items: []domain.SharedFile{{File: active, URL: "https://x/a"}, {File: revoked, URL: "https://x/b"}},
		Usage: domain.Usage{MailboxBytes: 7, MailboxActive: 1, TenantBytes: 70}, Policy: testPolicy(),
	}}
	h := newTestHandler(t, uc)
	rec := httptest.NewRecorder()
	h.InternalRoutes().ServeHTTP(rec, internalRequest(http.MethodGet, InternalPrefix+"/files", nil, domain.Owner{TenantID: uuid.New(), MailboxID: uuid.New()}))
	var out listDTO
	if err := json.Unmarshal(decode(t, rec).Data, &out); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("%d %v", rec.Code, err)
	}
	if !out.Enabled || len(out.Items) != 2 || out.Items[0].URL != "https://x/a" || out.Items[1].URL != "" || out.Items[1].State != "revoked" ||
		out.Usage.TenantBytes != 70 || out.Limits.MaxFileBytes != 1000 || out.Limits.MaxActivePerMailbox != 20 {
		t.Fatalf("listado: %+v", out)
	}
	rec = httptest.NewRecorder()
	h.InternalRoutes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, InternalPrefix+"/meta", nil))
	var meta metaDTO
	_ = json.Unmarshal(decode(t, rec).Data, &meta)
	if !meta.Enabled || meta.Limits.DefaultExpiryDays != 7 || meta.Limits.TenantQuotaBytes != 9000 {
		t.Fatalf("meta: %+v", meta)
	}
}

func TestRevocar(t *testing.T) {
	uc := &fakeUC{policy: testPolicy(), file: activeFile("a.pdf")}
	h := newTestHandler(t, uc)
	owner := domain.Owner{TenantID: uuid.New(), MailboxID: uuid.New()}
	rec := httptest.NewRecorder()
	h.InternalRoutes().ServeHTTP(rec, internalRequest(http.MethodDelete, InternalPrefix+"/files/"+uuid.NewString(), nil, owner))
	var out fileDTO
	_ = json.Unmarshal(decode(t, rec).Data, &out)
	if rec.Code != http.StatusOK || out.State != "revoked" || out.URL != "" {
		t.Fatalf("%d %+v", rec.Code, out)
	}
	rec = httptest.NewRecorder()
	h.InternalRoutes().ServeHTTP(rec, internalRequest(http.MethodDelete, InternalPrefix+"/files/no-es-uuid", nil, owner))
	if rec.Code != http.StatusNotFound {
		t.Errorf("id ilegible: %d", rec.Code)
	}
	uc.revokeErr = domain.ErrNotRevocable
	rec = httptest.NewRecorder()
	h.InternalRoutes().ServeHTTP(rec, internalRequest(http.MethodDelete, InternalPrefix+"/files/"+uuid.NewString(), nil, owner))
	if rec.Code != http.StatusUnprocessableEntity || decode(t, rec).Error.Code != "FILE_NOT_ACTIVE" {
		t.Fatalf("no revocable: %d", rec.Code)
	}
}

func publicURL(f domain.File) string {
	return domain.DownloadPath + "/" + f.TenantID.String() + "/" + f.ID.String() + "?x=1900000000&s=" + strings.Repeat("c", 64)
}

func TestPaginaDelEnlaceEscapaElNombreYNoCuenta(t *testing.T) {
	f := activeFile(`<script>alert(1)</script>.pdf`)
	uc := &fakeUC{file: f}
	h := newTestHandler(t, uc)
	rec := httptest.NewRecorder()
	h.PublicRoutes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, publicURL(f), nil))
	body := rec.Body.String()
	if rec.Code != http.StatusOK || strings.Contains(body, "<script>") || !strings.Contains(body, "&lt;script&gt;") {
		t.Fatalf("%d %s", rec.Code, body)
	}
	if !strings.Contains(body, `method="post"`) || !strings.Contains(body, "Descargar") || !strings.Contains(body, "noindex") {
		t.Fatalf("pagina: %s", body)
	}
	if rec.Header().Get("Cache-Control") != "no-store" || uc.claims.FileID != f.ID || uc.sig != strings.Repeat("c", 64) {
		t.Fatalf("cabeceras o claims: %v %+v", rec.Header(), uc.claims)
	}
}

func TestEnlaceInvalidoMismaRespuesta(t *testing.T) {
	f := activeFile("a.pdf")
	h := newTestHandler(t, &fakeUC{file: f, inspectErr: domain.ErrLinkInvalid, downloadErr: domain.ErrLinkInvalid})
	for _, target := range []string{publicURL(f), domain.DownloadPath + "/x/y?x=1&s=2"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			rec := httptest.NewRecorder()
			h.PublicRoutes().ServeHTTP(rec, httptest.NewRequest(method, target, nil))
			if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "Enlace no valido") {
				t.Errorf("%s %s: %d", method, target, rec.Code)
			}
		}
	}
	h = newTestHandler(t, &fakeUC{file: f, downloadErr: domain.ErrUnavailable})
	rec := httptest.NewRecorder()
	h.PublicRoutes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, publicURL(f), nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("indisponible: %d", rec.Code)
	}
}

func TestDescargaComoAdjuntoConCabecerasSeguras(t *testing.T) {
	f := activeFile("../año \"final\"\r\n.html")
	f.Name, _ = domain.SafeFileName(f.Name)
	uc := &fakeUC{file: f, content: "<html>"}
	h := newTestHandler(t, uc)
	rec := httptest.NewRecorder()
	h.PublicRoutes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, publicURL(f), nil))
	hd := rec.Header()
	want := map[string]string{
		"Content-Type":            "application/octet-stream",
		"Content-Disposition":     `attachment; filename="a_o _final_.html"; filename*=UTF-8''a%C3%B1o%20_final_.html`,
		"X-Content-Type-Options":  "nosniff",
		"Content-Length":          "7",
		"Cache-Control":           "no-store",
		"Content-Security-Policy": downloadCSP,
	}
	if rec.Code != http.StatusOK || rec.Body.String() != "<html>" {
		t.Fatalf("%d %q", rec.Code, rec.Body.String())
	}
	for k, v := range want {
		if hd.Get(k) != v {
			t.Errorf("%s = %q; se esperaba %q", k, hd.Get(k), v)
		}
	}
	if !strings.HasPrefix(hd.Get("Repr-Digest"), "sha-256=:") {
		t.Errorf("Repr-Digest: %q", hd.Get("Repr-Digest"))
	}
}

func TestHumanSize(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 1023: "1023 B", 1536: "1,5 KiB", 100 << 20: "100,0 MiB", 3 << 30: "3,0 GiB"} {
		if got := humanSize(n); got != want {
			t.Errorf("humanSize(%d) = %q", n, got)
		}
	}
}

func TestNewHandlerExigePlazos(t *testing.T) {
	if _, err := NewHandler(&fakeUC{}, Config{}, zap.NewNop()); err == nil {
		t.Fatal("sin plazos aceptado")
	}
}
