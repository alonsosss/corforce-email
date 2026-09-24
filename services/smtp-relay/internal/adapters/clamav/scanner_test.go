package clamav

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/clamav"
	"github.com/alonsosss/corforce-email/services/smtp-relay/internal/domain"
)

type stubEngine struct{ err error }

func (s stubEngine) Scan(context.Context, []byte) error { return s.err }

func TestScan(t *testing.T) {
	for engineErr, want := range map[error]error{
		fmt.Errorf("%w: Eicar-Signature", clamav.ErrInfected): domain.ErrInfected,
		fmt.Errorf("%w: sin conexion", clamav.ErrUnavailable): domain.ErrScanUnavailable,
		errors.New("respuesta rara"):                          domain.ErrScanUnavailable,
	} {
		if err := (&Scanner{engine: stubEngine{engineErr}}).Scan(context.Background(), []byte("x")); !errors.Is(err, want) {
			t.Errorf("%v: %v", engineErr, err)
		}
	}
	if err := (&Scanner{engine: stubEngine{}}).Scan(context.Background(), []byte("x")); err != nil {
		t.Errorf("limpio: %v", err)
	}
	if New("127.0.0.1:1", 0).Scan(context.Background(), []byte("x")) == nil {
		t.Error("sin clamd no hay veredicto limpio")
	}
}
