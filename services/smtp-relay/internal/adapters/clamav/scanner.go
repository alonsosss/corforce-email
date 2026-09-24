// Package clamav analiza el mensaje entero con el clamd de la celda (pkg/clamav): clamd decodifica
// el MIME y revisa cada adjunto, tambien los que vienen dentro de un mensaje reenviado.
package clamav

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/alonsosss/corforce-email/pkg/clamav"
	"github.com/alonsosss/corforce-email/services/smtp-relay/internal/domain"
)

type engine interface {
	Scan(ctx context.Context, data []byte) error
}

// Scanner implementa ports.Scanner. Falla cerrado: sin un OK explicito el mensaje no sale.
type Scanner struct {
	engine engine
}

func New(addr string, timeout time.Duration) *Scanner {
	return &Scanner{engine: clamav.New(addr, timeout)}
}

func (s *Scanner) Scan(ctx context.Context, raw []byte) error {
	err := s.engine.Scan(ctx, raw)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, clamav.ErrInfected):
		return fmt.Errorf("%w: %v", domain.ErrInfected, err)
	default:
		return fmt.Errorf("%w: %v", domain.ErrScanUnavailable, err)
	}
}
