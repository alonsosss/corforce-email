package app

import (
	"net/netip"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-migration/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/ports"
	"github.com/google/uuid"
)

type fixture struct {
	uc        *UseCase
	repo      *apptest.Repo
	events    *apptest.Events
	resolver  *apptest.Resolver
	tenants   *apptest.Tenants
	mailboxes *apptest.Mailboxes
	now       time.Time
	tenant    uuid.UUID
	actor     uuid.UUID
}

func newFixture(t *testing.T, mutate func(*Config)) *fixture {
	t.Helper()
	f := &fixture{
		repo: apptest.NewRepo(), events: &apptest.Events{}, tenants: &apptest.Tenants{},
		resolver: &apptest.Resolver{Addrs: []netip.Addr{netip.MustParseAddr("93.184.216.34")}},
		now:      time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC), tenant: uuid.New(), actor: uuid.New(),
	}
	f.tenants.IDs = []uuid.UUID{f.tenant}
	cfg := Config{
		RunnerConfigured: true, MaxActivePerTenant: 2, Lease: 90 * time.Second, MaxAttempts: 3, SweepInterval: 30 * time.Second,
		Source: domain.SourcePolicy{Ports: []int{143, 993}},
	}
	if mutate != nil {
		mutate(&cfg)
	}
	mb := &apptest.Mailboxes{Ref: ports.MailboxRef{Username: "ana@acme.test", Active: true}}
	f.mailboxes = mb
	f.uc = New(Deps{
		Repo: f.repo, Tx: apptest.Tx{}, Mailboxes: mb, Resolver: f.resolver, Cipher: apptest.Cipher{}, Tenants: f.tenants,
		Events: f.events, Config: cfg, Now: func() time.Time { return f.now },
	})
	return f
}

func validInput() CreateInput {
	return CreateInput{MailboxID: uuid.New(), Source: domain.Source{
		Host: "imap.origen.example", Port: 993, TLS: domain.TLSImplicit, Username: "ana@origen.example", Password: "clave-de-origen-123",
	}}
}
