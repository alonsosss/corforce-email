package http

import (
	"context"
	"slices"
	"strings"
	"sync"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"github.com/alonsosss/corforce-email/services/webmail/internal/ports"
)

// stubPrimarySenders hace de mail-directory para los remitentes fijados en Principal.
type stubPrimarySenders struct {
	mu    sync.Mutex
	rows  map[string][]string
	limit int
}

var _ ports.PrimarySenderDirectory = (*stubPrimarySenders)(nil)

func newStubPrimarySenders() *stubPrimarySenders {
	return &stubPrimarySenders{rows: map[string][]string{}}
}

func (s *stubPrimarySenders) PrimarySenders(_ context.Context, username string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.rows[username]), nil
}

func (s *stubPrimarySenders) AddPrimarySender(_ context.Context, username, address string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	address = strings.ToLower(strings.TrimSpace(address))
	if !strings.Contains(address, "@") {
		return &domain.ValidationError{Field: "address", Reason: "no es una dirección de correo válida"}
	}
	if slices.Contains(s.rows[username], address) {
		return nil
	}
	if s.limit > 0 && len(s.rows[username]) >= s.limit {
		return domain.ErrPrimarySenderLimit
	}
	s.rows[username] = append(s.rows[username], address)
	return nil
}

func (s *stubPrimarySenders) RemovePrimarySender(_ context.Context, username, address string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[username] = slices.DeleteFunc(s.rows[username], func(a string) bool { return a == strings.ToLower(address) })
	return nil
}
