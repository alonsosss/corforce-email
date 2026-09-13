package app

import (
	"errors"
	"testing"

	"github.com/alonsosss/corforce-email/services/automations/internal/domain"
	"github.com/alonsosss/corforce-email/services/automations/internal/ports"
	"github.com/google/uuid"
)

func TestUnEventoCreaUnaEjecucionPorFlujoYEsIdempotente(t *testing.T) {
	f := newFixture(t, Config{})
	a := f.activeWorkflow(t, contactCreated, false, sendEmail())
	b := f.activeWorkflow(t, contactCreated, true, sendEmail())
	other := f.activeWorkflow(t, domain.Trigger{Type: domain.TriggerConsentGranted}, false, sendEmail())
	draft := f.draft(t, "Borrador", sendEmail())

	ev := domain.TriggerEvent{EventID: uuid.NewString(), TenantID: f.tenant, Type: domain.TriggerContactCreated, ContactID: uuid.New()}
	n, err := f.uc.HandleTrigger(ctx, ev)
	if err != nil || n != 2 {
		t.Fatalf("entra en los dos flujos activos de su disparador: %d %v", n, err)
	}
	if len(f.store.RunsOf(a.ID)) != 1 || len(f.store.RunsOf(b.ID)) != 1 || len(f.store.RunsOf(other.ID)) != 0 || len(f.store.RunsOf(draft.ID)) != 0 {
		t.Fatal("ejecuciones por flujo")
	}
	if n, err := f.uc.HandleTrigger(ctx, ev); err != nil || n != 0 {
		t.Fatalf("la reentrega del evento no crea nada: %d %v", n, err)
	}
	if _, err := f.uc.HandleTrigger(ctx, domain.TriggerEvent{EventID: "", TenantID: f.tenant, ContactID: uuid.New()}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("evento sin id: %v", err)
	}
}

func TestReentrada(t *testing.T) {
	f := newFixture(t, Config{})
	once := f.activeWorkflow(t, contactCreated, false, sendEmail())
	again := f.activeWorkflow(t, contactCreated, true, sendEmail())
	contact := uuid.New()
	f.enter(t, contact)
	f.enter(t, contact)
	if len(f.store.RunsOf(once.ID)) != 1 {
		t.Fatalf("sin reentrada, una sola vez: %d", len(f.store.RunsOf(once.ID)))
	}
	if len(f.store.RunsOf(again.ID)) != 2 {
		t.Fatalf("con reentrada, una por evento: %d", len(f.store.RunsOf(again.ID)))
	}
	// Desactivar la reentrada no deja entrar otra vez a quien ya recorrio el flujo.
	if _, err := f.uc.PauseWorkflow(ctx, f.tenant, again.ID, ""); err != nil {
		t.Fatal(err)
	}
	off := false
	if _, err := f.uc.UpdateWorkflow(ctx, f.tenant, again.ID, domain.Patch{ReEntry: &off}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.uc.ActivateWorkflow(ctx, f.tenant, again.ID); err != nil {
		t.Fatal(err)
	}
	f.enter(t, contact)
	if len(f.store.RunsOf(again.ID)) != 2 {
		t.Fatalf("sin reentrada no entra quien ya estuvo: %d", len(f.store.RunsOf(again.ID)))
	}
}

func TestFiltroPorLista(t *testing.T) {
	f := newFixture(t, Config{})
	list := uuid.New()
	w, err := f.uc.CreateWorkflow(ctx, f.tenant, domain.NewWorkflowInput{
		Name: "Con lista", Trigger: contactCreated, ListID: &list, Steps: []domain.Step{sendEmail()}, CreatedBy: f.user,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.uc.ActivateWorkflow(ctx, f.tenant, w.ID); err != nil {
		t.Fatal(err)
	}
	outside, member := uuid.New(), uuid.New()
	f.contacts.Members[list] = map[uuid.UUID]bool{member: true}
	if f.enter(t, outside) != 0 || f.enter(t, member) != 1 {
		t.Fatal("solo entra quien es miembro de la lista")
	}

	f.contacts.Err = ports.ErrUnavailable
	ev := domain.TriggerEvent{EventID: uuid.NewString(), TenantID: f.tenant, Type: domain.TriggerContactCreated, ContactID: member}
	if _, err := f.uc.HandleTrigger(ctx, ev); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatalf("contacts caido: se reintenta: %v", err)
	}
	if done, _ := (f.store.Processed[ev.EventID]); done != uuid.Nil {
		t.Fatal("un evento que no se pudo procesar no queda marcado")
	}
	f.contacts.Err = nil
	f.contacts.MissingLists[list] = true
	if n, err := f.uc.HandleTrigger(ctx, ev); err != nil || n != 0 {
		t.Fatalf("una lista borrada deja fuera sin reintentar: %d %v", n, err)
	}
}

func TestClicPorCampanaYSinAutoDisparo(t *testing.T) {
	f := newFixture(t, Config{})
	campaign := uuid.New()
	byCampaign := f.activeWorkflow(t, domain.Trigger{Type: domain.TriggerEmailClicked, CampaignID: &campaign}, true, sendEmail())
	anyClick := f.activeWorkflow(t, domain.Trigger{Type: domain.TriggerEmailClicked}, true, sendEmail())
	another := f.activeWorkflow(t, domain.Trigger{Type: domain.TriggerEmailClicked}, true, sendEmail())
	click := func(c uuid.UUID) int {
		t.Helper()
		n, err := f.uc.HandleTrigger(ctx, domain.TriggerEvent{
			EventID: uuid.NewString(), TenantID: f.tenant, Type: domain.TriggerEmailClicked, ContactID: uuid.New(), CampaignID: &c,
		})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if click(uuid.New()) != 2 || click(campaign) != 3 {
		t.Fatal("la campana concreta filtra; sin ella entra cualquier clic")
	}
	if click(anyClick.ID) != 1 {
		t.Fatal("un clic en el correo del propio flujo solo dispara a los otros")
	}
	if len(f.store.RunsOf(byCampaign.ID)) != 1 || len(f.store.RunsOf(anyClick.ID)) != 2 || len(f.store.RunsOf(another.ID)) != 3 {
		t.Fatalf("ejecuciones: %d %d %d", len(f.store.RunsOf(byCampaign.ID)), len(f.store.RunsOf(anyClick.ID)), len(f.store.RunsOf(another.ID)))
	}
}
