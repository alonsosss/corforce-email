package app

import (
	"context"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/automations/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/automations/internal/domain"
	"github.com/google/uuid"
)

var ctx = context.Background()

type fixture struct {
	uc        *UseCase
	clock     *apptest.Clock
	store     *apptest.Store
	sender    *apptest.Sender
	contacts  *apptest.Contacts
	templates *apptest.Templates
	rules     *apptest.Rules
	messages  *apptest.RunMessages
	scans     *apptest.DateScans
	tenant    uuid.UUID
	user      uuid.UUID
}

func newFixture(t *testing.T, cfg Config) *fixture {
	t.Helper()
	clock := &apptest.Clock{T: time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)}
	f := &fixture{
		clock: clock, store: apptest.NewStore(clock), sender: &apptest.Sender{}, contacts: apptest.NewContacts(),
		templates: &apptest.Templates{Kind: KindMarketing, Version: 3}, tenant: uuid.New(), user: uuid.New(),
		rules: apptest.NewRules(), messages: &apptest.RunMessages{}, scans: apptest.NewDateScans(),
	}
	if cfg.PublicBaseURL == "" {
		cfg.PublicBaseURL = "https://app.example.com"
	}
	f.uc = New(Deps{
		Settings: apptest.Settings{S: f.store}, Deliveries: apptest.Deliveries{S: f.store},
		Workflows: apptest.Workflows{S: f.store}, Runs: apptest.Runs{S: f.store}, Processed: apptest.Processed{S: f.store},
		Tx: f.store, Events: f.store, Sender: f.sender, Contacts: f.contacts, Templates: f.templates,
		Rules: f.rules, RunMessages: f.messages, DateScans: f.scans,
		Config: cfg, Now: clock.Now,
	})
	return f
}

func ptr[T any](v T) *T { return &v }

// sendable da de alta un contacto enviable en el doble de contacts.
func (f *fixture) sendable(email string) domain.Contact {
	c := domain.Contact{ID: uuid.New(), Email: email, FirstName: "Ana", LastName: "Diaz"}
	f.contacts.Sendables[c.ID] = c
	return c
}

func sendEmail() domain.Step {
	return domain.Step{Type: domain.StepSendEmail, TemplateID: ptr(uuid.New()), FromEmail: "news@shop.example.com", FromName: "Tienda"}
}

// activeWorkflow crea y activa un flujo (templates responde marketing, version 3).
func (f *fixture) activeWorkflow(t *testing.T, trigger domain.Trigger, reEntry bool, steps ...domain.Step) *domain.Workflow {
	t.Helper()
	w, err := f.uc.CreateWorkflow(ctx, f.tenant, domain.NewWorkflowInput{
		Name: "Flujo " + uuid.NewString()[:8], Trigger: trigger, ReEntry: reEntry, Steps: steps, CreatedBy: f.user,
	})
	if err != nil {
		t.Fatal(err)
	}
	if w, err = f.uc.ActivateWorkflow(ctx, f.tenant, w.ID); err != nil {
		t.Fatal(err)
	}
	return w
}

// enter hace entrar al contacto por un contacts.contact.created.
func (f *fixture) enter(t *testing.T, contactID uuid.UUID) int {
	t.Helper()
	n, err := f.uc.HandleTrigger(ctx, domain.TriggerEvent{
		EventID: uuid.NewString(), TenantID: f.tenant, Type: domain.TriggerContactCreated, ContactID: contactID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func (f *fixture) tick(t *testing.T) {
	t.Helper()
	if err := f.uc.Tick(ctx, f.tenant); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) onlyRun(t *testing.T, w *domain.Workflow) domain.Run {
	t.Helper()
	runs := f.store.RunsOf(w.ID)
	if len(runs) != 1 {
		t.Fatalf("se esperaba una ejecucion, hay %d", len(runs))
	}
	return runs[0]
}

var contactCreated = domain.Trigger{Type: domain.TriggerContactCreated}
