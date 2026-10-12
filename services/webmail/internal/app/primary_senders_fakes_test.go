package app

import (
	"context"
	"slices"
	"strings"
	"sync"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"github.com/alonsosss/corforce-email/services/webmail/internal/ports"
)

// fakePrimarySenders hace de mail-directory para los remitentes fijados en Principal.
type fakePrimarySenders struct {
	mu    sync.Mutex
	rows  map[string][]string
	limit int
	err   error
	reads int
}

var _ ports.PrimarySenderDirectory = (*fakePrimarySenders)(nil)

func newFakePrimarySenders() *fakePrimarySenders {
	return &fakePrimarySenders{rows: map[string][]string{}}
}

func (f *fakePrimarySenders) PrimarySenders(_ context.Context, username string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if f.err != nil {
		return nil, f.err
	}
	return slices.Clone(f.rows[username]), nil
}

func (f *fakePrimarySenders) AddPrimarySender(_ context.Context, username, address string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	address = strings.ToLower(strings.TrimSpace(address))
	if !strings.Contains(address, "@") {
		return &domain.ValidationError{Field: "address", Reason: "no es una dirección de correo válida"}
	}
	if slices.Contains(f.rows[username], address) {
		return nil
	}
	if f.limit > 0 && len(f.rows[username]) >= f.limit {
		return domain.ErrPrimarySenderLimit
	}
	f.rows[username] = append(f.rows[username], address)
	return nil
}

func (f *fakePrimarySenders) RemovePrimarySender(_ context.Context, username, address string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows[username] = slices.DeleteFunc(f.rows[username], func(a string) bool { return a == strings.ToLower(address) })
	return nil
}
