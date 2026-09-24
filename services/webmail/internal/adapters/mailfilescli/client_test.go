package mailfilescli

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

var mb = domain.MailboxRef{TenantID: "11111111-1111-4111-8111-111111111111", MailboxID: "22222222-2222-4222-8222-222222222222"}

func server(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, "token-interno", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func checkIdentity(t *testing.T, r *http.Request) {
	t.Helper()
	if r.Header.Get("X-Gateway-Token") != "token-interno" || r.Header.Get(tenantHeader) != mb.TenantID || r.Header.Get(mailboxHeader) != mb.MailboxID {
		t.Errorf("identidad: %v", r.Header)
	}
}

func TestSubidaEnFlujoConNombreYOpciones(t *testing.T) {
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		checkIdentity(t, r)
		body, _ := io.ReadAll(r.Body)
		q := r.URL.Query()
		if r.Method != http.MethodPost || r.URL.Path != filesPath || q.Get("name") != "año & planos.dwg" ||
			q.Get("expires_in_days") != "3" || q.Get("max_downloads") != "" || string(body) != "contenido" ||
			r.Header.Get("Content-Type") != "application/octet-stream" {
			t.Errorf("peticion: %s %s %v %q", r.Method, r.URL, q, body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"data":{"id":"f1","name":"año & planos.dwg","size_bytes":9,"url":"https://x/e","state":"active","remaining_downloads":20,"expires_at":"2026-10-01T00:00:00Z"}}`)
	})
	f, err := c.UploadLargeFile(context.Background(), mb, "año & planos.dwg", strings.NewReader("contenido"), domain.LargeFileOptions{ExpiresInDays: 3})
	if err != nil || f.ID != "f1" || f.URL != "https://x/e" || f.RemainingDownloads != 20 || f.ExpiresAt.IsZero() {
		t.Fatalf("subida: %v %+v", err, f)
	}
}

func TestLosRechazosDeMailFilesLleganTalCual(t *testing.T) {
	c := server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInsufficientStorage)
		_, _ = io.WriteString(w, `{"error":{"code":"MAILBOX_FILES_QUOTA_EXCEEDED","message":"no cabe"}}`)
	})
	_, err := c.UploadLargeFile(context.Background(), mb, "a", strings.NewReader("x"), domain.LargeFileOptions{})
	var rejection *domain.ServiceRejection
	if !errors.As(err, &rejection) || rejection.Kind != domain.RejectQuota || rejection.Code != "MAILBOX_FILES_QUOTA_EXCEEDED" {
		t.Fatalf("rechazo: %v", err)
	}
}

func TestListadoYRevocacion(t *testing.T) {
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		checkIdentity(t, r)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == filesPath:
			_, _ = io.WriteString(w, `{"data":{"enabled":true,"items":[{"id":"f1","state":"expired"}],"usage":{"mailbox_bytes":5,"mailbox_active":1,"tenant_bytes":50},"limits":{"max_file_bytes":104857600,"max_expiry_days":30,"max_downloads":100,"mailbox_quota_bytes":2147483648}}}`)
		case r.Method == http.MethodDelete && r.URL.Path == filesPath+"/f1":
			_, _ = io.WriteString(w, `{"data":{"id":"f1","state":"revoked"}}`)
		default:
			t.Errorf("peticion inesperada: %s %s", r.Method, r.URL)
		}
	})
	l, err := c.ListLargeFiles(context.Background(), mb)
	if err != nil || !l.Enabled || len(l.Items) != 1 || l.Items[0].State != "expired" || l.Usage.TenantBytes != 50 ||
		l.Limits.MaxFileBytes != 104857600 || l.Limits.MailboxQuotaBytes != 2147483648 {
		t.Fatalf("listado: %v %+v", err, l)
	}
	f, err := c.RevokeLargeFile(context.Background(), mb, "f1")
	if err != nil || f.State != "revoked" {
		t.Fatalf("revocar: %v %+v", err, f)
	}
}

func TestSinRespuestaEsIndisponibilidad(t *testing.T) {
	c := server(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) })
	if _, err := c.ListLargeFiles(context.Background(), mb); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("502: %v", err)
	}
	if _, err := New("ftp://x", "t", time.Second); err == nil {
		t.Fatal("URL invalida aceptada")
	}
}
