package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	handler "github.com/alonsosss/corforce-email/services/mail-files/internal/adapters/http"
	"github.com/alonsosss/corforce-email/services/mail-files/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

var settingsEnv = []string{
	"MAIL_FILES_PORT", "MAIL_LINK_SIGNING_KEY", "PUBLIC_BASE_URL", "MAIL_FILES_CLAMD_ADDR", "MAIL_FILES_CLAMD_TIMEOUT",
	"MAIL_FILES_SPOOL_DIR", "MAIL_FILES_UPLOAD_TIMEOUT", "MAIL_FILES_DOWNLOAD_TIMEOUT", "MAIL_FILES_RATE_LIMIT_PER_MIN",
	"MAIL_FILES_PUBLIC_RATE_LIMIT_PER_MIN", "MAIL_FILES_MAX_FILE_BYTES", "MAIL_FILES_MAX_EXPIRY_DAYS", "MAIL_FILES_DEFAULT_EXPIRY_DAYS",
	"MAIL_FILES_MAX_DOWNLOADS", "MAIL_FILES_DEFAULT_MAX_DOWNLOADS", "MAIL_FILES_MAILBOX_QUOTA_BYTES", "MAIL_FILES_TENANT_QUOTA_BYTES",
	"MAIL_FILES_MAX_ACTIVE_PER_MAILBOX", "MAIL_FILES_MAX_CONCURRENT_UPLOADS", "MAIL_FILES_SWEEP_INTERVAL", "MAIL_FILES_PENDING_GRACE",
	"MAIL_FILES_HISTORY_RETENTION",
}

func setEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for _, k := range settingsEnv {
		t.Setenv(k, "")
	}
	t.Setenv("ENVIRONMENT", "production")
	t.Setenv("INTERNAL_GATEWAY_TOKEN", "token-interno")
	t.Setenv("MAIL_LINK_SIGNING_KEY", strings.Repeat("k", 32))
	t.Setenv("PUBLIC_BASE_URL", "https://correo.example.com")
	t.Setenv("MAIL_FILES_CLAMD_ADDR", "clamd:3310")
	for k, v := range kv {
		t.Setenv(k, v)
	}
}

func TestValoresPorDefecto(t *testing.T) {
	setEnv(t, nil)
	st, err := loadSettings()
	if err != nil {
		t.Fatal(err)
	}
	p := st.app.Policy
	if p.MaxFileBytes != 100<<20 || p.DefaultExpiryDays != 7 || p.MaxExpiryDays != 30 || p.DefaultMaxDownloads != 20 ||
		p.MaxDownloads != 100 || p.MailboxQuotaBytes != 2<<30 || p.TenantQuotaBytes != 20<<30 || p.MaxActivePerMailbox != 200 {
		t.Fatalf("politica: %+v", p)
	}
	if st.spoolDir != defaultSpoolDir || st.downloadTimeout != 30*time.Minute || st.app.DeleteGrace != st.downloadTimeout ||
		st.app.SweepInterval != 15*time.Minute || st.port != 8061 {
		t.Fatalf("ajustes: %+v", st)
	}
}

func TestLoQueNoPuedeFaltar(t *testing.T) {
	for key, value := range map[string]string{
		"MAIL_LINK_SIGNING_KEY":  "corta",
		"PUBLIC_BASE_URL":        " ",
		"MAIL_FILES_CLAMD_ADDR":  " ",
		"INTERNAL_GATEWAY_TOKEN": "",
	} {
		setEnv(t, map[string]string{key: value})
		if _, err := loadSettings(); err == nil {
			t.Errorf("%s=%q aceptado", key, value)
		}
	}
}

func TestTopesFueraDeRango(t *testing.T) {
	for key, value := range map[string]string{
		// Mas que lo que clamd analiza entero: el fichero se quedaria sin veredicto.
		"MAIL_FILES_MAX_FILE_BYTES":        "209715200",
		"MAIL_FILES_DEFAULT_EXPIRY_DAYS":   "31",
		"MAIL_FILES_DEFAULT_MAX_DOWNLOADS": "101",
		"MAIL_FILES_MAILBOX_QUOTA_BYTES":   "2097152",
		"MAIL_FILES_TENANT_QUOTA_BYTES":    "1048576",
		"MAIL_FILES_SWEEP_INTERVAL":        "30s",
		"MAIL_FILES_DOWNLOAD_TIMEOUT":      "10s",
	} {
		setEnv(t, map[string]string{key: value})
		if _, err := loadSettings(); err == nil {
			t.Errorf("%s=%s aceptado", key, value)
		}
	}
	setEnv(t, map[string]string{"MAIL_FILES_SWEEP_INTERVAL": "0"})
	if st, err := loadSettings(); err != nil || st.app.SweepInterval != 0 {
		t.Fatalf("barrido desactivado: %v", err)
	}
}

type stubUC struct{}

func (stubUC) Enabled() bool         { return true }
func (stubUC) Policy() domain.Policy { return domain.Policy{MaxFileBytes: 10} }
func (stubUC) Upload(context.Context, domain.Owner, string, io.Reader, domain.UploadOptions) (domain.SharedFile, error) {
	return domain.SharedFile{}, domain.ErrUnavailable
}
func (stubUC) List(context.Context, domain.Owner) (domain.Listing, error) {
	return domain.Listing{}, nil
}
func (stubUC) Revoke(context.Context, domain.Owner, uuid.UUID) (domain.SharedFile, error) {
	return domain.SharedFile{}, domain.ErrNotFound
}
func (stubUC) Inspect(context.Context, domain.LinkClaims, string) (domain.File, error) {
	return domain.File{}, domain.ErrLinkInvalid
}
func (stubUC) Download(context.Context, domain.LinkClaims, string) (domain.File, io.ReadCloser, error) {
	return domain.File{}, nil, domain.ErrLinkInvalid
}

func TestCadenaDeLaPeticion(t *testing.T) {
	setEnv(t, nil)
	st, err := loadSettings()
	if err != nil {
		t.Fatal(err)
	}
	h, err := handler.NewHandler(stubUC{}, handler.Config{UploadTimeout: time.Minute, DownloadTimeout: time.Minute, OperationTimeout: time.Second}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	r := router(h, st, zap.NewNop())
	do := func(method, target string, headers map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, nil)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	gw := map[string]string{"X-Gateway-Token": "token-interno"}
	if rec := do(http.MethodGet, handler.InternalPrefix+"/meta", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("sin token del gateway: %d", rec.Code)
	}
	if rec := do(http.MethodGet, handler.InternalPrefix+"/meta", gw); rec.Code != http.StatusOK {
		t.Fatalf("meta: %d", rec.Code)
	}
	// Una persona con sesion de la plataforma no llega a la API interna.
	withUser := map[string]string{"X-Gateway-Token": "token-interno", "X-User-ID": uuid.NewString()}
	if rec := do(http.MethodGet, handler.InternalPrefix+"/meta", withUser); rec.Code != http.StatusForbidden {
		t.Fatalf("persona en la API interna: %d", rec.Code)
	}
	target := domain.DownloadPath + "/" + uuid.NewString() + "/" + uuid.NewString() + "?x=1&s=" + strings.Repeat("a", 64)
	if rec := do(http.MethodGet, target, gw); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "Enlace no válido") {
		t.Fatalf("enlace publico: %d", rec.Code)
	}
	if rec := do(http.MethodGet, "/api/v1/otra-cosa", gw); rec.Code != http.StatusNotFound {
		t.Fatalf("ruta desconocida: %d", rec.Code)
	}
}
