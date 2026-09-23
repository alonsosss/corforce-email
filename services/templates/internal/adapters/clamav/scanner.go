// Package clamav analiza las imagenes subidas con el clamd de la celda (pkg/clamav) y traduce su
// veredicto a los errores del dominio de templates.
package clamav

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/alonsosss/corforce-email/pkg/clamav"
	"github.com/alonsosss/corforce-email/services/templates/internal/domain"
)

type engine interface {
	Scan(ctx context.Context, data []byte) error
}

// Scanner implementa ports.VirusScanner. Falla cerrado: sin un OK explicito de clamd la imagen
// no se guarda.
type Scanner struct {
	engine engine
}

func New(addr string, timeout time.Duration) *Scanner {
	return &Scanner{engine: clamav.New(addr, timeout)}
}

func (s *Scanner) Scan(ctx context.Context, data []byte) error {
	err := s.engine.Scan(ctx, data)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, clamav.ErrInfected):
		return fmt.Errorf("%w: %v", domain.ErrAssetRejected, err)
	default:
		return fmt.Errorf("%w: %v", domain.ErrScannerUnavailable, err)
	}
}
