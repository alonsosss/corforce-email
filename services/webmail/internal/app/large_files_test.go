package app

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

type fakeLargeFiles struct {
	calls int
	mb    domain.MailboxRef
	body  string
	err   error
}

func (f *fakeLargeFiles) ListLargeFiles(_ context.Context, mb domain.MailboxRef) (domain.LargeFileListing, error) {
	f.calls++
	f.mb = mb
	return domain.LargeFileListing{Enabled: true}, f.err
}

func (f *fakeLargeFiles) UploadLargeFile(_ context.Context, mb domain.MailboxRef, _ string, body io.Reader, _ domain.LargeFileOptions) (domain.LargeFile, error) {
	f.calls++
	f.mb = mb
	data, _ := io.ReadAll(body)
	f.body = string(data)
	return domain.LargeFile{ID: "f1"}, f.err
}

func (f *fakeLargeFiles) RevokeLargeFile(_ context.Context, mb domain.MailboxRef, id string) (domain.LargeFile, error) {
	f.calls++
	f.mb = mb
	return domain.LargeFile{ID: id}, f.err
}

func largeFilesHarness(t *testing.T, files *fakeLargeFiles) *Service {
	t.Helper()
	h := newHarness(t)
	deps := h.deps()
	if files != nil {
		deps.LargeFiles = files
	}
	svc, err := New(deps)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestFicherosGrandesConLaEmpresaYElBuzonDeLaSesion(t *testing.T) {
	files := &fakeLargeFiles{}
	svc := largeFilesHarness(t, files)
	ctx := context.Background()
	if _, err := svc.UploadLargeFile(ctx, davSession(), "a.pdf", strings.NewReader("contenido"), domain.LargeFileOptions{}); err != nil || files.mb.TenantID != testTenant || files.mb.MailboxID != testMailbox || files.body != "contenido" {
		t.Fatalf("subida: %v %+v", err, files)
	}
	if l, err := svc.LargeFiles(ctx, davSession()); err != nil || !l.Enabled {
		t.Fatalf("listado: %v", err)
	}
	if _, err := svc.RevokeLargeFile(ctx, davSession(), "33333333-3333-4333-8333-333333333333"); err != nil {
		t.Fatal(err)
	}
	calls := files.calls
	var rejection *domain.ServiceRejection
	if _, err := svc.RevokeLargeFile(ctx, davSession(), "../../internal"); !errors.As(err, &rejection) || rejection.Kind != domain.RejectNotFound || files.calls != calls {
		t.Fatalf("id ilegible: %v", err)
	}
}

func TestFicherosGrandesSinEmpresaEnLaSesionNiMailFiles(t *testing.T) {
	files := &fakeLargeFiles{}
	svc := largeFilesHarness(t, files)
	legacy := domain.Session{Username: testUser}
	if _, err := svc.LargeFiles(context.Background(), legacy); !errors.Is(err, domain.ErrSessionInvalid) || files.calls != 0 {
		t.Fatalf("sesion sin empresa: %v", err)
	}
	off := largeFilesHarness(t, nil)
	if l, err := off.LargeFiles(context.Background(), davSession()); err != nil || l.Enabled || l.Items == nil {
		t.Fatalf("sin mail-files: %v %+v", err, l)
	}
	if _, err := off.UploadLargeFile(context.Background(), davSession(), "a", strings.NewReader("x"), domain.LargeFileOptions{}); !errors.Is(err, domain.ErrLargeFilesDisabled) {
		t.Fatalf("subida sin mail-files: %v", err)
	}
}

func TestLosFallosDeMailFilesSonIndisponibilidadSalvoSusRechazos(t *testing.T) {
	files := &fakeLargeFiles{err: errors.New("conexion rehusada")}
	svc := largeFilesHarness(t, files)
	if _, err := svc.LargeFiles(context.Background(), davSession()); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("caida: %v", err)
	}
	files.err = &domain.ServiceRejection{Kind: domain.RejectValidation, Code: "FILE_INFECTED"}
	var rejection *domain.ServiceRejection
	if _, err := svc.UploadLargeFile(context.Background(), davSession(), "a", strings.NewReader("x"), domain.LargeFileOptions{}); !errors.As(err, &rejection) || rejection.Code != "FILE_INFECTED" {
		t.Fatalf("rechazo: %v", err)
	}
}
