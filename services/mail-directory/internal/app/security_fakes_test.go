package app

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
)

// GenerateRecoveryCode da codigos distintos y validos en cada llamada.
func (f *fakeSecrets) GenerateRecoveryCode() (string, error) {
	f.seq++
	code := []byte("AAAAAAAAAA")
	for i, n := len(code)-1, f.seq; n > 0 && i >= 0; i, n = i-1, n/len(domain.RecoveryCodeAlphabet) {
		code[i] = domain.RecoveryCodeAlphabet[n%len(domain.RecoveryCodeAlphabet)]
	}
	return string(code), nil
}

func (f *fakeDomains) OwnedNames(_ context.Context, tenantID uuid.UUID, names []string) ([]string, error) {
	var out []string
	for _, n := range names {
		owned := slices.ContainsFunc(f.items, func(d *domain.Domain) bool { return d.TenantID == tenantID && d.Domain == n })
		if !owned && f.aliasDomains != nil {
			owned = slices.ContainsFunc(f.aliasDomains.items, func(a *domain.AliasDomain) bool {
				return a.TenantID == tenantID && a.AliasDomain == n
			})
		}
		if owned {
			out = append(out, n)
		}
	}
	return out, nil
}

func (f *fakeMailboxes) SetMFAEnabled(_ context.Context, tenantID, id uuid.UUID, enabled bool) error {
	for _, m := range f.items {
		if m.TenantID == tenantID && m.ID == id {
			m.MFAEnabled = enabled
			return nil
		}
	}
	return domain.ErrNotFound
}

func (f *fakeFilters) ListByTenant(_ context.Context, tenantID uuid.UUID) ([]domain.MailboxFilters, error) {
	var out []domain.MailboxFilters
	for _, v := range f.items {
		if v.TenantID == tenantID {
			c := *v
			c.Rules = slices.Clone(v.Rules)
			c.Forwarding.Addresses = slices.Clone(v.Forwarding.Addresses)
			out = append(out, c)
		}
	}
	slices.SortFunc(out, func(a, b domain.MailboxFilters) int {
		if a.Username < b.Username {
			return -1
		}
		return 1
	})
	return out, nil
}

// fakeMFA imita mail.mailbox_mfa, con AdvanceStep y ConsumeRecoveryCode atomicos como en SQL.
type fakeMFA struct {
	rows map[uuid.UUID]*domain.MailboxMFA
}

func (f *fakeMFA) Get(_ context.Context, tenantID, mailboxID uuid.UUID) (*domain.MailboxMFA, error) {
	m, ok := f.rows[mailboxID]
	if !ok || m.TenantID != tenantID {
		return nil, domain.ErrNotFound
	}
	c := *m
	c.RecoveryHashes = slices.Clone(m.RecoveryHashes)
	return &c, nil
}

func (f *fakeMFA) Create(_ context.Context, m *domain.MailboxMFA) error {
	if _, ok := f.rows[m.MailboxID]; ok {
		return domain.ErrAlreadyExists
	}
	c := *m
	f.rows[m.MailboxID] = &c
	return nil
}

func (f *fakeMFA) AdvanceStep(_ context.Context, tenantID, mailboxID uuid.UUID, step int64) (bool, error) {
	m, ok := f.rows[mailboxID]
	if !ok || m.TenantID != tenantID || m.LastStep >= step {
		return false, nil
	}
	m.LastStep = step
	return true, nil
}

func (f *fakeMFA) ConsumeRecoveryCode(_ context.Context, tenantID, mailboxID uuid.UUID, hash string) (int, bool, error) {
	m, ok := f.rows[mailboxID]
	if !ok || m.TenantID != tenantID {
		return 0, false, nil
	}
	i := slices.Index(m.RecoveryHashes, hash)
	if i < 0 {
		return 0, false, nil
	}
	m.RecoveryHashes = slices.Delete(m.RecoveryHashes, i, i+1)
	return len(m.RecoveryHashes), true, nil
}

func (f *fakeMFA) ReplaceRecoveryCodes(_ context.Context, _, mailboxID uuid.UUID, hashes []string) error {
	m, ok := f.rows[mailboxID]
	if !ok {
		return domain.ErrNotFound
	}
	m.RecoveryHashes = slices.Clone(hashes)
	return nil
}

func (f *fakeMFA) Delete(_ context.Context, tenantID, mailboxID uuid.UUID) (bool, error) {
	m, ok := f.rows[mailboxID]
	if !ok || m.TenantID != tenantID {
		return false, nil
	}
	delete(f.rows, mailboxID)
	return true, nil
}

type fakePolicies struct {
	rows  map[uuid.UUID]*domain.MailPolicy
	locks []bool
}

func (f *fakePolicies) Lock(_ context.Context, _ uuid.UUID, exclusive bool) error {
	f.locks = append(f.locks, exclusive)
	return nil
}

func (f *fakePolicies) Get(_ context.Context, tenantID uuid.UUID) (*domain.MailPolicy, error) {
	p, ok := f.rows[tenantID]
	if !ok {
		return nil, domain.ErrNotFound
	}
	c := *p
	return &c, nil
}

func (f *fakePolicies) Upsert(_ context.Context, p *domain.MailPolicy) error {
	c := *p
	f.rows[p.TenantID] = &c
	return nil
}

// fakeSealer antepone los datos autenticados: el secreto solo se abre con el id de su buzon.
type fakeSealer struct{}

func (fakeSealer) Seal(plain, aad []byte) ([]byte, error) {
	return append(append(append([]byte{}, aad...), ':'), plain...), nil
}

func (fakeSealer) Open(sealed, aad []byte) ([]byte, error) {
	prefix := append(append([]byte{}, aad...), ':')
	if !bytes.HasPrefix(sealed, prefix) {
		return nil, errors.New("datos autenticados distintos")
	}
	return sealed[len(prefix):], nil
}

// fakeTOTP acepta los codigos que la prueba le da, cada uno con su paso.
type fakeTOTP struct {
	codes map[string]int64
	// secrets anota con que secreto se pregunto.
	secrets []string
}

func (f *fakeTOTP) ValidateStep(secret, code string, _ time.Time) (int64, bool) {
	f.secrets = append(f.secrets, secret)
	step, ok := f.codes[code]
	return step, ok
}

type forwardingEvent struct {
	username string
	change   domain.ForwardingChange
}

func (f *fakeEvents) MailboxMFAEnabled(_ context.Context, m *domain.Mailbox, _ time.Time) error {
	return f.record("mail.mailbox.mfa_enabled")
}

func (f *fakeEvents) MailboxMFADisabled(_ context.Context, _ *domain.Mailbox, _ time.Time, by string, actorID *uuid.UUID) error {
	if err := f.record("mail.mailbox.mfa_disabled"); err != nil {
		return err
	}
	f.mfaDisabledBy = append(f.mfaDisabledBy, by)
	if actorID != nil {
		f.mfaActors = append(f.mfaActors, *actorID)
	}
	return nil
}

func (f *fakeEvents) MailboxForwardingChanged(_ context.Context, m *domain.Mailbox, _ time.Time, c domain.ForwardingChange) error {
	if err := f.record("mail.mailbox.forwarding_changed"); err != nil {
		return err
	}
	f.forwarding = append(f.forwarding, forwardingEvent{username: m.Username, change: c})
	return nil
}

func (f *fakeEvents) MailPolicyUpdated(_ context.Context, _ *domain.MailPolicy, removed int) error {
	if err := f.record("mail.policy.updated"); err != nil {
		return err
	}
	f.policyRemoved = append(f.policyRemoved, removed)
	return nil
}
