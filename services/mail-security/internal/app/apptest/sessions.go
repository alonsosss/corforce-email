package apptest

import (
	"context"
	"sync"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
)

// SessionCall es una llamada a EngineSessions.ForgetCredentials.
type SessionCall struct {
	Username string
	Kick     bool
}

// EngineSessions implementa ports.EngineSessions en memoria: anota cada llamada y devuelve Err.
type EngineSessions struct {
	mu    sync.Mutex
	Calls []SessionCall
	Err   error
}

func (f *EngineSessions) ForgetCredentials(_ context.Context, username string, kick bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, SessionCall{Username: username, Kick: kick})
	return f.Err
}

// SessionMetrics implementa ports.SessionRevocationMetrics en memoria.
type SessionMetrics struct {
	mu      sync.Mutex
	Revoked map[domain.SessionAction]int
	Failed  map[domain.SessionRevocationFailure]int
}

func NewSessionMetrics() *SessionMetrics {
	return &SessionMetrics{Revoked: map[domain.SessionAction]int{}, Failed: map[domain.SessionRevocationFailure]int{}}
}

func (m *SessionMetrics) SessionsRevoked(action domain.SessionAction) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Revoked[action]++
}

func (m *SessionMetrics) SessionRevocationFailed(reason domain.SessionRevocationFailure) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Failed[reason]++
}
