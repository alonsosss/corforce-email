package http

import (
	"context"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

// unlimited deja pasar todo: las pruebas que no son del limitador no deben tropezar con el.
type unlimited struct{}

func (unlimited) AllowIP(context.Context, string) (bool, time.Duration)  { return true, 0 }
func (unlimited) AllowKey(context.Context, string) (bool, time.Duration) { return true, 0 }

// countingLimiter cuenta por clave y admite rate peticiones de cada una.
type countingLimiter struct {
	mu    sync.Mutex
	rate  int
	seen  map[string]int
	reset time.Duration
}

func newCountingLimiter(rate int) *countingLimiter {
	return &countingLimiter{rate: rate, seen: map[string]int{}, reset: 1500 * time.Millisecond}
}

func (l *countingLimiter) hit(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seen[key]++
	return l.seen[key] <= l.rate, l.reset
}

func (l *countingLimiter) AllowIP(_ context.Context, ip string) (bool, time.Duration) {
	return l.hit("ip:" + ip)
}

func (l *countingLimiter) AllowKey(_ context.Context, key string) (bool, time.Duration) {
	return l.hit("k:" + key)
}

func (l *countingLimiter) count(key string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.seen[key]
}

// stubMFAStore guarda en memoria los desafios y las preparaciones.
type stubMFAStore struct {
	mu         sync.Mutex
	challenges map[string]domain.MFAChallenge
	attempts   map[string]int
	setups     map[string]string
}

func newStubMFAStore() *stubMFAStore {
	return &stubMFAStore{challenges: map[string]domain.MFAChallenge{}, attempts: map[string]int{}, setups: map[string]string{}}
}

func (s *stubMFAStore) CreateChallenge(_ context.Context, key string, c domain.MFAChallenge, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.challenges[key] = c
	return nil
}

func (s *stubMFAStore) GetChallenge(_ context.Context, key string) (domain.MFAChallenge, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.challenges[key]
	if !ok {
		return domain.MFAChallenge{}, domain.ErrMFAChallengeExpired
	}
	return c, nil
}

func (s *stubMFAStore) CountAttempt(_ context.Context, key string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.challenges[key]; !ok {
		return 0, domain.ErrMFAChallengeExpired
	}
	s.attempts[key]++
	return s.attempts[key], nil
}

func (s *stubMFAStore) DeleteChallenge(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.challenges, key)
	return nil
}

func (s *stubMFAStore) SaveSetup(_ context.Context, username, hash string, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setups[username] = hash
	return nil
}

func (s *stubMFAStore) SetupHash(_ context.Context, username string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.setups[username]
	if !ok {
		return "", domain.ErrMFASetupExpired
	}
	return h, nil
}

func (s *stubMFAStore) DeleteSetup(_ context.Context, username string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.setups, username)
	return nil
}

// stubSecurity hace de mail-directory: validCode es el unico codigo aceptado y la verificacion en
// dos pasos esta activa para quien la tenga en mail-auth (mfaUser). filtersErr lo devuelve SetFilters
// a traves de stubSettings.err.
type stubSecurity struct {
	mu        sync.Mutex
	validCode string
	enabled   bool
	err       error
	created   *domain.AppPasswordInput
	deleted   string
}

func (s *stubSecurity) MFAStatus(context.Context, string) (domain.MFAStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return domain.MFAStatus{Enabled: s.enabled, RecoveryRemaining: 7}, s.err
}

func (s *stubSecurity) ActivateMFA(_ context.Context, _, _, code string) ([]string, error) {
	if code != s.validCode {
		return nil, domain.ErrInvalidMFACode
	}
	return []string{"AAAAA-BBBBB", "CCCCC-DDDDD"}, nil
}

func (s *stubSecurity) VerifyMFA(_ context.Context, _, code string) (domain.MFAVerification, error) {
	if code != s.validCode {
		return domain.MFAVerification{}, domain.ErrInvalidMFACode
	}
	return domain.MFAVerification{Method: "totp"}, nil
}

func (s *stubSecurity) RegenerateRecoveryCodes(_ context.Context, _, code string) ([]string, error) {
	if code != s.validCode {
		return nil, domain.ErrInvalidMFACode
	}
	return []string{"EEEEE-FFFFF"}, nil
}

func (s *stubSecurity) DisableMFA(_ context.Context, _, code string) error {
	if code != s.validCode {
		return domain.ErrInvalidMFACode
	}
	return nil
}

func (s *stubSecurity) AppPasswords(context.Context, string) (domain.AppPasswordList, error) {
	created := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	return domain.AppPasswordList{Items: []domain.AppPassword{{
		ID: "33333333-3333-4333-8333-333333333333", Name: "Movil", Active: true, CreatedAt: created,
		Access: domain.AppPasswordAccess{IMAP: true, SMTP: true},
	}}}, s.err
}

func (s *stubSecurity) CreateAppPassword(_ context.Context, _ string, in domain.AppPasswordInput) (domain.CreatedAppPassword, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return domain.CreatedAppPassword{}, s.err
	}
	s.created = &in
	return domain.CreatedAppPassword{
		AppPassword: domain.AppPassword{ID: "44444444-4444-4444-8444-444444444444", Name: in.Name, Access: in.Access, Active: true},
		Password:    "abcd-efgh-ijkl-mnop",
	}, nil
}

func (s *stubSecurity) DeleteAppPassword(_ context.Context, _, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleted = id
	return s.err
}

type stubTOTP struct{}

func (stubTOTP) NewSecret() (string, error) { return "JBSWY3DPEHPK3PXP", nil }
func (stubTOTP) ProvisioningURI(secret, account string) string {
	return "otpauth://totp/Prueba:" + account + "?secret=" + secret
}
