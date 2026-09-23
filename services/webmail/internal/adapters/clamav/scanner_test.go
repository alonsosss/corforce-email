package clamav

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/clamav"
	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

type stubEngine struct{ err error }

func (s stubEngine) Scan(context.Context, []byte) error { return s.err }

func TestScanTraduceElVeredictoAlDominio(t *testing.T) {
	if err := (&Scanner{engine: stubEngine{}}).Scan(context.Background(), "a.pdf", []byte("x")); err != nil {
		t.Fatalf("limpio: %v", err)
	}
	infected := fmt.Errorf("%w: Eicar-Test-Signature", clamav.ErrInfected)
	err := (&Scanner{engine: stubEngine{infected}}).Scan(context.Background(), "eicar.com", []byte("x"))
	if !errors.Is(err, domain.ErrAttachmentInfected) || !strings.Contains(err.Error(), "Eicar-Test-Signature") || !strings.Contains(err.Error(), "eicar.com") {
		t.Fatalf("infectado: %v", err)
	}
	for _, cause := range []error{clamav.ErrUnavailable, errors.New("otro fallo")} {
		if err := (&Scanner{engine: stubEngine{cause}}).Scan(context.Background(), "a.txt", []byte("x")); !errors.Is(err, domain.ErrScanUnavailable) {
			t.Fatalf("%v debe fallar cerrado: %v", cause, err)
		}
	}
}
