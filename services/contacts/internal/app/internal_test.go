package app

import (
	"context"
	"errors"
	"testing"

	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/google/uuid"
)

func TestEnviablesPorIDSoloDevuelveLosEnviablesDeLaEmpresa(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	ana := f.addContact(t, "ana@example.com", domain.StatusActive, domain.ConsentGranted)
	sinConsentimiento := f.addContact(t, "eva@example.com", domain.StatusActive, domain.ConsentNone)
	baja := f.addContact(t, "luis@example.com", domain.StatusUnsubscribed, domain.ConsentRevoked)
	rebote := f.addContact(t, "rebote@example.com", domain.StatusBounced, domain.ConsentGranted)
	ajeno := &domain.Contact{TenantID: uuid.New(), Email: "otra@example.com", Status: domain.StatusActive, ConsentStatus: domain.ConsentGranted, Source: domain.SourceAPI}
	if err := (fakeContacts{f.s}).Insert(ctx, ajeno); err != nil {
		t.Fatal(err)
	}

	got, err := f.uc.SendableContacts(ctx, f.tenant, []uuid.UUID{ana.ID, sinConsentimiento.ID, baja.ID, rebote.ID, ajeno.ID, ana.ID, uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != ana.ID {
		t.Fatalf("solo el contacto activo con consentimiento vigente y de la empresa: %+v", got)
	}
	for _, c := range got {
		if !c.Sendable() {
			t.Fatalf("la consulta y la regla del dominio no pueden discrepar: %+v", c)
		}
	}

	if _, err := f.uc.SendableContacts(ctx, f.tenant, nil); !errors.Is(err, domain.ErrInvalidContactIDs) {
		t.Fatalf("sin ids: %v", err)
	}
	many := make([]uuid.UUID, MaxSendableIDs+1)
	for i := range many {
		many[i] = uuid.New()
	}
	if _, err := f.uc.SendableContacts(ctx, f.tenant, many); !errors.Is(err, domain.ErrInvalidContactIDs) {
		t.Fatalf("por encima del tope: %v", err)
	}
}

func TestPertenenciaALaListaYAltaYBajaDeMiembros(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	list, err := f.uc.CreateList(ctx, f.tenant, "Bienvenida", "")
	if err != nil {
		t.Fatal(err)
	}
	a := f.addContact(t, "a@example.com", domain.StatusActive, domain.ConsentGranted)
	b := f.addContact(t, "b@example.com", domain.StatusActive, domain.ConsentNone)

	if _, err := f.uc.AddMembers(ctx, f.tenant, list.ID, []uuid.UUID{a.ID}); err != nil {
		t.Fatal(err)
	}
	got, err := f.uc.ListMembersAmong(ctx, f.tenant, list.ID, []uuid.UUID{a.ID, b.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != a.ID {
		t.Fatalf("solo a es miembro: %v", got)
	}
	// La pertenencia no depende de que el contacto sea enviable.
	if _, err := f.uc.AddMembers(ctx, f.tenant, list.ID, []uuid.UUID{b.ID}); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.uc.ListMembersAmong(ctx, f.tenant, list.ID, []uuid.UUID{b.ID}); len(got) != 1 {
		t.Fatalf("b tambien es miembro: %v", got)
	}
	if _, err := f.uc.RemoveMembers(ctx, f.tenant, list.ID, []uuid.UUID{a.ID}); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.uc.ListMembersAmong(ctx, f.tenant, list.ID, []uuid.UUID{a.ID}); len(got) != 0 {
		t.Fatalf("a salio de la lista: %v", got)
	}
	if _, err := f.uc.ListMembersAmong(ctx, f.tenant, uuid.New(), []uuid.UUID{a.ID}); !errors.Is(err, domain.ErrListNotFound) {
		t.Fatalf("lista inexistente: %v", err)
	}
	if _, err := f.uc.ListMembersAmong(ctx, f.tenant, list.ID, nil); !errors.Is(err, domain.ErrInvalidContactIDs) {
		t.Fatalf("sin ids: %v", err)
	}
}
