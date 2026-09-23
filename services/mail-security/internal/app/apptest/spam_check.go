package apptest

import (
	"context"
	"sync"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
)

// SpamScanner implementa ports.SpamScanner en memoria: devuelve lo que la prueba prepare y guarda los
// mensajes recibidos.
type SpamScanner struct {
	mu       sync.Mutex
	Result   domain.SpamCheckResult
	Err      error
	Messages [][]byte
}

func (s *SpamScanner) Check(_ context.Context, msg []byte) (domain.SpamCheckResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Messages = append(s.Messages, append([]byte(nil), msg...))
	out := s.Result
	out.Symbols = append([]domain.SpamCheckSymbol(nil), s.Result.Symbols...)
	return out, s.Err
}

// SpamCheckMetrics implementa ports.SpamCheckMetrics y cuenta cada desenlace.
type SpamCheckMetrics struct {
	mu       sync.Mutex
	outcomes map[domain.SpamCheckOutcome]int
}

func (m *SpamCheckMetrics) SpamChecked(outcome domain.SpamCheckOutcome) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.outcomes == nil {
		m.outcomes = map[domain.SpamCheckOutcome]int{}
	}
	m.outcomes[outcome]++
}

func (m *SpamCheckMetrics) Count(outcome domain.SpamCheckOutcome) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.outcomes[outcome]
}
