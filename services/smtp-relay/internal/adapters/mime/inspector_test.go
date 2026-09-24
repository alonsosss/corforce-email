package mime

import (
	"errors"
	"testing"

	"github.com/alonsosss/corforce-email/services/smtp-relay/internal/domain"
)

func TestInspect(t *testing.T) {
	i := New(4 << 10)
	got, err := i.Inspect([]byte("From: a@empresa.test\r\nMessage-ID: <x1@empresa.test>\r\n\r\nhola\r\n"))
	if err != nil || got.HasAttachments || got.MessageID != "x1@empresa.test" {
		t.Fatalf("texto: %+v %v", got, err)
	}
	raw := "From: a@empresa.test\r\nContent-Type: multipart/mixed; boundary=b\r\n\r\n--b\r\nContent-Type: image/png\r\nContent-Transfer-Encoding: base64\r\n\r\niVBORw0KGgo=\r\n--b--\r\n"
	if got, err := i.Inspect([]byte(raw)); err != nil || !got.HasAttachments {
		t.Fatalf("imagen: %+v %v", got, err)
	}
	if _, err := i.Inspect([]byte("sin cabeceras")); !errors.Is(err, domain.ErrMalformed) {
		t.Fatalf("malformado: %v", err)
	}
	if _, err := i.Inspect(make([]byte, 5<<10)); !errors.Is(err, domain.ErrTooLarge) {
		t.Fatalf("tamano: %v", err)
	}
}
