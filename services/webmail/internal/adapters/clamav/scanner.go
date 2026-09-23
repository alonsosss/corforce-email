// Package clamav analiza adjuntos con el clamd de la celda (pkg/clamav) y traduce su veredicto a
// los errores del dominio del webmail.
package clamav

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/alonsosss/corforce-email/pkg/clamav"
	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

type engine interface {
	Scan(ctx context.Context, data []byte) error
}

// Scanner implementa ports.VirusScanner. Falla cerrado: cualquier respuesta que no sea un OK
// explicito es domain.ErrScanUnavailable (incluido superar StreamMaxLength de clamd).
type Scanner struct {
	engine engine
}

func New(addr string, timeout time.Duration) *Scanner {
	return &Scanner{engine: clamav.New(addr, timeout)}
}

func (s *Scanner) Scan(ctx context.Context, name string, data []byte) error {
	err := s.engine.Scan(ctx, data)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, clamav.ErrInfected):
		return fmt.Errorf("%w: %v en %q", domain.ErrAttachmentInfected, err, name)
	default:
		return fmt.Errorf("%w: %v", domain.ErrScanUnavailable, err)
	}
}
