package http

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/app"
	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"go.uber.org/zap"
)

// stubLargeFiles hace de mail-files: anota con que buzon, nombre, contenido y opciones se le llamo.
type stubLargeFiles struct {
	mu      sync.Mutex
	mb      domain.MailboxRef
	name    string
	content string
	opts    domain.LargeFileOptions
	revoked string
	err     error
}

func (s *stubLargeFiles) ListLargeFiles(_ context.Context, mb domain.MailboxRef) (domain.LargeFileListing, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mb = mb
	return domain.LargeFileListing{
		Enabled: true,
		Items:   []domain.LargeFile{{ID: "f1", Name: "planos.dwg", URL: "https://x/enlace", State: "active", SizeBytes: 9}},
		Usage:   domain.LargeFileUsage{MailboxBytes: 9, MailboxActive: 1, TenantBytes: 90},
		Limits:  domain.LargeFileLimits{MaxFileBytes: 1 << 20, MaxExpiryDays: 30, MaxDownloads: 100},
	}, s.err
}

func (s *stubLargeFiles) UploadLargeFile(_ context.Context, mb domain.MailboxRef, name string, body io.Reader, opts domain.LargeFileOptions) (domain.LargeFile, error) {
	data, err := io.ReadAll(body)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		return domain.LargeFile{}, domain.ErrUnavailable
	}
	s.mb, s.name, s.content, s.opts = mb, name, string(data), opts
	if s.err != nil {
		return domain.LargeFile{}, s.err
	}
	return domain.LargeFile{ID: "f2", Name: name, SizeBytes: int64(len(data)), URL: "https://x/nuevo", State: "active"}, nil
}

func (s *stubLargeFiles) RevokeLargeFile(_ context.Context, mb domain.MailboxRef, id string) (domain.LargeFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mb, s.revoked = mb, id
	return domain.LargeFile{ID: id, State: "revoked"}, s.err
}

func newLargeFilesEnv(t *testing.T, files *stubLargeFiles, maxBytes int64) http.Handler {
	t.Helper()
	deps := testDeps(&memStore{m: map[string]domain.Session{}}, &stubMailbox{}, nopSender{}, &stubVacations{}, &stubAddressBook{}, newStubSettings(), &stubDAV{})
	if files != nil {
		deps.LargeFiles = files
	}
	svc, err := app.New(deps)
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewHandler(svc, Config{
		CookieSecure: true, SessionIdle: 30 * time.Minute, SessionMax: 12 * time.Hour,
		AllowedOrigins:  []string{allowedOrigin},
		MaxMessageBytes: 4096, OperationTimeout: 5 * time.Second, TransferTimeout: 5 * time.Second,
		MaxLargeFileBytes: maxBytes,
	}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	return h.Routes()
}

func largeFileForm(t *testing.T, field, name, content string) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile(field, name)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(part, content)
	_ = w.Close()
	return &buf, w.FormDataContentType()
}

func TestSubidaDeFicheroGrandeVaAMailFilesConElBuzonDeLaSesion(t *testing.T) {
	files := &stubLargeFiles{}
	h := newLargeFilesEnv(t, files, 1<<20)
	cookie := login(t, h)
	body, ctype := largeFileForm(t, "file", "planos final.dwg", "contenido")
	rec := do(h, http.MethodPost, BasePath+"/large-files?expires_in_days=3&max_downloads=4", body,
		map[string]string{"Origin": allowedOrigin, "Content-Type": ctype}, cookie)
	if rec.Code != http.StatusCreated {
		t.Fatalf("subida: %d %s", rec.Code, rec.Body.String())
	}
	if files.mb.TenantID != testTenant || files.mb.MailboxID != testMailbox || files.name != "planos final.dwg" ||
		files.content != "contenido" || files.opts != (domain.LargeFileOptions{ExpiresInDays: 3, MaxDownloads: 4}) {
		t.Fatalf("llamada: %+v", files)
	}
	var out struct {
		Data largeFileDTO `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Data.URL != "https://x/nuevo" {
		t.Fatalf("respuesta: %v %s", err, rec.Body.String())
	}
}

func TestSubidaDeFicheroGrandeExigeOrigenYSesion(t *testing.T) {
	files := &stubLargeFiles{}
	h := newLargeFilesEnv(t, files, 1<<20)
	cookie := login(t, h)
	body, ctype := largeFileForm(t, "file", "a.txt", "x")
	if rec := do(h, http.MethodPost, BasePath+"/large-files", body, map[string]string{"Content-Type": ctype}, cookie); rec.Code != http.StatusForbidden {
		t.Fatalf("sin Origin: %d", rec.Code)
	}
	body, ctype = largeFileForm(t, "file", "a.txt", "x")
	if rec := do(h, http.MethodPost, BasePath+"/large-files", body, map[string]string{"Origin": allowedOrigin, "Content-Type": ctype}, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("sin sesion: %d", rec.Code)
	}
	if files.content != "" {
		t.Fatal("llego a mail-files sin sesion u origen")
	}
}

func TestSubidaDeFicheroGrandeRechazos(t *testing.T) {
	files := &stubLargeFiles{}
	h := newLargeFilesEnv(t, files, 16)
	cookie := login(t, h)
	origin := func(ctype string) map[string]string {
		return map[string]string{"Origin": allowedOrigin, "Content-Type": ctype}
	}

	body, ctype := largeFileForm(t, "otro", "a.txt", "x")
	if rec := do(h, http.MethodPost, BasePath+"/large-files", body, origin(ctype), cookie); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("campo ajeno: %d", rec.Code)
	}
	if rec := do(h, http.MethodPost, BasePath+"/large-files", strings.NewReader("x"), origin("text/plain"), cookie); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("sin multipart: %d", rec.Code)
	}
	body, ctype = largeFileForm(t, "file", "a.txt", "x")
	if rec := do(h, http.MethodPost, BasePath+"/large-files?expires_in_days=cero", body, origin(ctype), cookie); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("caducidad ilegible: %d", rec.Code)
	}
	// Mas que el techo mas el margen del multipart: se corta en la lectura y se responde 413, no 503.
	body, ctype = largeFileForm(t, "file", "a.txt", strings.Repeat("x", 2<<20))
	rec := do(h, http.MethodPost, BasePath+"/large-files", body, origin(ctype), cookie)
	if rec.Code != http.StatusRequestEntityTooLarge || errorCode(t, rec) != "FILE_TOO_LARGE" {
		t.Fatalf("demasiado grande: %d %s", rec.Code, rec.Body.String())
	}
	// Sin Content-Length el tope salta a mitad del flujo, ya camino de mail-files.
	body, ctype = largeFileForm(t, "file", "a.txt", strings.Repeat("x", 2<<20))
	rec = do(h, http.MethodPost, BasePath+"/large-files", io.MultiReader(body), origin(ctype), cookie)
	if rec.Code != http.StatusRequestEntityTooLarge || errorCode(t, rec) != "FILE_TOO_LARGE" {
		t.Fatalf("demasiado grande en flujo: %d %s", rec.Code, rec.Body.String())
	}
	// Los rechazos de mail-files llegan tal cual.
	files.err = &domain.ServiceRejection{Kind: domain.RejectValidation, Code: "FILE_INFECTED", Message: "virus"}
	body, ctype = largeFileForm(t, "file", "a.txt", "x")
	rec = do(h, http.MethodPost, BasePath+"/large-files", body, origin(ctype), cookie)
	if rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != "FILE_INFECTED" {
		t.Fatalf("infectado: %d %s", rec.Code, rec.Body.String())
	}
	files.err = &domain.ServiceRejection{Kind: domain.RejectQuota, Code: "MAILBOX_FILES_QUOTA_EXCEEDED", Message: "cuota"}
	body, ctype = largeFileForm(t, "file", "a.txt", "x")
	if rec := do(h, http.MethodPost, BasePath+"/large-files", body, origin(ctype), cookie); rec.Code != http.StatusInsufficientStorage {
		t.Fatalf("cuota: %d", rec.Code)
	}
}

func TestListadoYRevocacionDeFicherosGrandes(t *testing.T) {
	files := &stubLargeFiles{}
	h := newLargeFilesEnv(t, files, 1<<20)
	cookie := login(t, h)
	rec := do(h, http.MethodGet, BasePath+"/large-files", nil, nil, cookie)
	var list struct {
		Data largeFileListingDTO `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("listado: %d %s", rec.Code, rec.Body.String())
	}
	if !list.Data.Enabled || len(list.Data.Items) != 1 || list.Data.Usage.TenantBytes != 90 || list.Data.Limits.MaxDownloads != 100 {
		t.Fatalf("listado: %+v", list.Data)
	}
	id := "33333333-3333-4333-8333-333333333333"
	rec = do(h, http.MethodDelete, BasePath+"/large-files/"+id, nil, map[string]string{"Origin": allowedOrigin}, cookie)
	if rec.Code != http.StatusOK || files.revoked != id {
		t.Fatalf("revocar: %d %s", rec.Code, rec.Body.String())
	}
	files.revoked = ""
	rec = do(h, http.MethodDelete, BasePath+"/large-files/no-es-un-id", nil, map[string]string{"Origin": allowedOrigin}, cookie)
	if rec.Code != http.StatusNotFound || files.revoked != "" {
		t.Fatalf("id ilegible: %d", rec.Code)
	}
}

func TestSinMailFilesLaFuncionEstaApagada(t *testing.T) {
	h := newLargeFilesEnv(t, nil, 1<<20)
	cookie := login(t, h)
	rec := do(h, http.MethodGet, BasePath+"/large-files", nil, nil, cookie)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"enabled":false`) || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("listado apagado: %d %s", rec.Code, rec.Body.String())
	}
	body, ctype := largeFileForm(t, "file", "a.txt", "x")
	rec = do(h, http.MethodPost, BasePath+"/large-files", body, map[string]string{"Origin": allowedOrigin, "Content-Type": ctype}, cookie)
	if rec.Code != http.StatusServiceUnavailable || errorCode(t, rec) != "LARGE_FILES_DISABLED" {
		t.Fatalf("subida apagada: %d %s", rec.Code, rec.Body.String())
	}
}
