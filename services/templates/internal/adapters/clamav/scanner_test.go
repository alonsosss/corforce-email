package clamav

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/clamav"
	"github.com/alonsosss/corforce-email/services/templates/internal/domain"
)

type stubEngine struct{ err error }

func (s stubEngine) Scan(context.Context, []byte) error { return s.err }

func TestScanTraduceElVeredictoAlDominio(t *testing.T) {
	if err := (&Scanner{engine: stubEngine{}}).Scan(context.Background(), []byte("x")); err != nil {
		t.Fatalf("limpia: %v", err)
	}
	infected := fmt.Errorf("%w: Eicar-Test-Signature", clamav.ErrInfected)
	if err := (&Scanner{engine: stubEngine{infected}}).Scan(context.Background(), []byte("x")); !errors.Is(err, domain.ErrAssetRejected) {
		t.Fatalf("infectada: %v", err)
	}
	for _, cause := range []error{clamav.ErrUnavailable, errors.New("otro fallo")} {
		if err := (&Scanner{engine: stubEngine{cause}}).Scan(context.Background(), []byte("x")); !errors.Is(err, domain.ErrScannerUnavailable) {
			t.Fatalf("%v debe fallar cerrado: %v", cause, err)
		}
	}
}
