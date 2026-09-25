package app

import (
	"context"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

// fakeMFAStore imita a Redis: los desafios caducan con el reloj de la prueba y los intentos se
// cuentan sobre el desafio vivo.
type fakeMFAStore struct {
	mu         sync.Mutex
	clock      *testClock
	challenges map[string]fakeChallenge
	setups     map[string]string
	lastTTL    time.Duration
	setupTTL   time.Duration
	failCreate error
	failDelete error
}

type fakeChallenge struct {
	c        domain.MFAChallenge
	attempts int
	deadline time.Time
}

func newFakeMFAStore(clock *testClock) *fakeMFAStore {
	return &fakeMFAStore{clock: clock, challenges: map[string]fakeChallenge{}, setups: map[string]string{}}
}

func (s *fakeMFAStore) live(key string) (fakeChallenge, bool) {
	ch, ok := s.challenges[key]
	if !ok || !s.clock.Now().Before(ch.deadline) {
		delete(s.challenges, key)
		return fakeChallenge{}, false
	}
	return ch, true
}

func (s *fakeMFAStore) CreateChallenge(_ context.Context, key string, c domain.MFAChallenge, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failCreate != nil {
		return s.failCreate
	}
	s.lastTTL = ttl
	s.challenges[key] = fakeChallenge{c: c, deadline: s.clock.Now().Add(ttl)}
	return nil
}

func (s *fakeMFAStore) GetChallenge(_ context.Context, key string) (domain.MFAChallenge, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch, ok := s.live(key)
	if !ok {
		return domain.MFAChallenge{}, domain.ErrMFAChallengeExpired
	}
	return ch.c, nil
}

func (s *fakeMFAStore) CountAttempt(_ context.Context, key string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch, ok := s.live(key)
	if !ok {
		return 0, domain.ErrMFAChallengeExpired
	}
	ch.attempts++
	s.challenges[key] = ch
	return ch.attempts, nil
}

func (s *fakeMFAStore) DeleteChallenge(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failDelete != nil {
		return s.failDelete
	}
	delete(s.challenges, key)
	return nil
}

func (s *fakeMFAStore) SaveSetup(_ context.Context, username, hash string, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setups[username] = hash
	s.setupTTL = ttl
	return nil
}

func (s *fakeMFAStore) SetupHash(_ context.Context, username string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.setups[username]
	if !ok {
		return "", domain.ErrMFASetupExpired
	}
	return h, nil
}

func (s *fakeMFAStore) DeleteSetup(_ context.Context, username string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.setups, username)
	return nil
}

func (s *fakeMFAStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.challenges)
}

// fakeSecurity hace de mail-directory: validCode es el unico codigo aceptado y, como el real, se
// gasta al usarse (un segundo uso es INVALID_MFA_CODE).
type fakeSecurity struct {
	mu        sync.Mutex
	enabled   bool
	validCode string
	used      map[string]bool
	err       error
	calls     []string
	activated string
	disabled  bool
	created   *domain.AppPasswordInput
	deleted   string
	list      domain.AppPasswordList
}

func (f *fakeSecurity) record(call string) {
	f.calls = append(f.calls, call)
}

func (f *fakeSecurity) check(code string) error {
	if f.err != nil {
		return f.err
	}
	if !f.enabled {
		return domain.ErrMFANotEnabled
	}
	if code != f.validCode || f.used[code] {
		return domain.ErrInvalidMFACode
	}
	if f.used == nil {
		f.used = map[string]bool{}
	}
	f.used[code] = true
	return nil
}

func (f *fakeSecurity) MFAStatus(context.Context, string) (domain.MFAStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("status")
	return domain.MFAStatus{Enabled: f.enabled, RecoveryRemaining: 10}, f.err
}

func (f *fakeSecurity) ActivateMFA(_ context.Context, _, secret, code string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("activate")
	if f.err != nil {
		return nil, f.err
	}
	if f.enabled {
		return nil, domain.ErrMFAAlreadyEnabled
	}
	if code != f.validCode {
		return nil, domain.ErrInvalidMFACode
	}
	f.enabled, f.activated = true, secret
	return []string{"AAAAA-BBBBB"}, nil
}

func (f *fakeSecurity) VerifyMFA(_ context.Context, _, code string) (domain.MFAVerification, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("verify")
	if err := f.check(code); err != nil {
		return domain.MFAVerification{}, err
	}
	return domain.MFAVerification{Method: "totp", RecoveryRemaining: 10}, nil
}

func (f *fakeSecurity) RegenerateRecoveryCodes(_ context.Context, _, code string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("recovery")
	if err := f.check(code); err != nil {
		return nil, err
	}
	return []string{"CCCCC-DDDDD"}, nil
}

func (f *fakeSecurity) DisableMFA(_ context.Context, _, code string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("disable")
	if err := f.check(code); err != nil {
		return err
	}
	f.enabled, f.disabled = false, true
	return nil
}

func (f *fakeSecurity) AppPasswords(context.Context, string) (domain.AppPasswordList, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("list")
	return f.list, f.err
}

func (f *fakeSecurity) CreateAppPassword(_ context.Context, _ string, in domain.AppPasswordInput) (domain.CreatedAppPassword, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("create")
	if f.err != nil {
		return domain.CreatedAppPassword{}, f.err
	}
	f.created = &in
	return domain.CreatedAppPassword{AppPassword: domain.AppPassword{ID: "33333333-3333-4333-8333-333333333333", Name: in.Name, Access: in.Access, Active: true}, Password: "clave-de-aplicacion"}, nil
}

func (f *fakeSecurity) DeleteAppPassword(_ context.Context, _, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("delete")
	f.deleted = id
	return f.err
}

type fakeTOTP struct{ secret string }

func (t fakeTOTP) NewSecret() (string, error) { return t.secret, nil }
func (t fakeTOTP) ProvisioningURI(secret, account string) string {
	return "otpauth://totp/Prueba:" + account + "?secret=" + secret
}
