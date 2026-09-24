package objectstore

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/objectstore"
	"github.com/alonsosss/corforce-email/services/mail-files/internal/domain"
)

type fakeObjects struct {
	contentType string
	data        map[string]string
	openErr     error
}

func (f *fakeObjects) Put(_ context.Context, key string, r io.Reader, _ int64, contentType string) error {
	b, _ := io.ReadAll(r)
	f.data[key], f.contentType = string(b), contentType
	return nil
}

func (f *fakeObjects) OpenLimited(_ context.Context, key string, maxBytes int64) (io.ReadCloser, objectstore.ObjectInfo, error) {
	if f.openErr != nil {
		return nil, objectstore.ObjectInfo{}, f.openErr
	}
	d, ok := f.data[key]
	if !ok {
		return nil, objectstore.ObjectInfo{}, objectstore.ErrNotFound
	}
	if int64(len(d)) > maxBytes {
		return nil, objectstore.ObjectInfo{Size: int64(len(d))}, objectstore.ErrTooLarge
	}
	return io.NopCloser(strings.NewReader(d)), objectstore.ObjectInfo{Size: int64(len(d))}, nil
}

func (f *fakeObjects) Delete(_ context.Context, key string) error {
	delete(f.data, key)
	return nil
}

func TestGuardaSiempreComoOctetStreamYAbreConElTamanoGuardado(t *testing.T) {
	objs := &fakeObjects{data: map[string]string{}}
	s := New(objs)
	ctx := context.Background()
	if err := s.Put(ctx, "private/t/mail-files/f", strings.NewReader("hola"), 4); err != nil {
		t.Fatal(err)
	}
	if objs.contentType != "application/octet-stream" {
		t.Fatalf("tipo: %s", objs.contentType)
	}
	rc, err := s.Open(ctx, "private/t/mail-files/f", 4)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(rc); string(b) != "hola" {
		t.Fatalf("contenido: %q", b)
	}
	if _, err := s.Open(ctx, "private/t/mail-files/f", 3); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("objeto mayor que lo guardado: %v", err)
	}
	objs.data["private/t/mail-files/f"] = "ho"
	if _, err := s.Open(ctx, "private/t/mail-files/f", 4); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("objeto cambiado por fuera: %v", err)
	}
	if _, err := s.Open(ctx, "private/no", 4); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("inexistente: %v", err)
	}
	objs.openErr = errors.New("minio caido")
	if _, err := s.Open(ctx, "private/t/mail-files/f", 4); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("almacen caido: %v", err)
	}
}
