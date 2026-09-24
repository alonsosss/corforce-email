// Package clamav analiza los ficheros compartidos con el clamd de la plataforma (pkg/clamav) en
// flujo y traduce su veredicto a los errores del dominio.
package clamav

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/alonsosss/corforce-email/pkg/clamav"
	"github.com/alonsosss/corforce-email/services/mail-files/internal/domain"
)

type engine interface {
	ScanReader(ctx context.Context, r io.Reader) error
}

// Scanner implementa ports.Scanner. Falla cerrado: sin un OK explicito de clamd el fichero no se
// guarda.
type Scanner struct {
	engine engine
}

func New(addr string, timeout time.Duration) *Scanner {
	return &Scanner{engine: clamav.New(addr, timeout)}
}

func (s *Scanner) Scan(ctx context.Context, r io.Reader) error {
	err := s.engine.ScanReader(ctx, r)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, clamav.ErrInfected):
		return fmt.Errorf("%w: %v", domain.ErrInfected, err)
	default:
		return fmt.Errorf("%w: %v", domain.ErrScanUnavailable, err)
	}
}
