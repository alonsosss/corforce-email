//go:build integration

package postgres

import (
	"testing"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/google/uuid"
)

// La consulta interna de enviables por id (automations, antes de cada envio) y la
// pertenencia a una lista contra la base real, con el consentimiento proyectado por el
// trigger de consents.
func TestEnviablesPorIDYPertenenciaALista(t *testing.T) {
	_, ctx := testPool(t)
	cp := &db.ContextPool{}
	contacts := NewContactRepository(cp)
	consents := NewConsentRepository(cp)
	lists := NewListRepository(cp)
	query := NewSegmentQuery(cp)
	tenant := uuid.New()

	grant := func(tenantID uuid.UUID, c *domain.Contact) {
		t.Helper()
		if err := consents.Append(ctx, &domain.Consent{TenantID: tenantID, ContactID: c.ID, Purpose: domain.PurposeMarketing,
			Status: domain.ConsentGranted, Method: domain.MethodForm, Source: "https://example.com/alta", IP: strp("203.0.113.4")}); err != nil {
			t.Fatal(err)
		}
	}
	ana := insert(t, ctx, contacts, newContact(tenant, "ana-enviable@example.com"))
	grant(tenant, ana)
	sinConsentimiento := insert(t, ctx, contacts, newContact(tenant, "eva-enviable@example.com"))
	baja := insert(t, ctx, contacts, newContact(tenant, "luis-enviable@example.com"))
	grant(tenant, baja)
	baja.Status = domain.StatusUnsubscribed
	if err := contacts.Update(ctx, baja); err != nil {
		t.Fatal(err)
	}
	otra := uuid.New()
	ajeno := insert(t, ctx, contacts, newContact(otra, "ajeno-enviable@example.com"))
	grant(otra, ajeno)

	got, err := query.Sendable(ctx, tenant, []uuid.UUID{ana.ID, sinConsentimiento.ID, baja.ID, ajeno.ID, uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != ana.ID || got[0].Email != ana.Email {
		t.Fatalf("solo el enviable de la empresa: %+v", got)
	}

	list := &domain.List{TenantID: tenant, Name: "Bienvenida enviables"}
	if err := lists.Create(ctx, list); err != nil {
		t.Fatal(err)
	}
	if _, err := lists.AddMembers(ctx, tenant, list.ID, []uuid.UUID{ana.ID, sinConsentimiento.ID, ajeno.ID}); err != nil {
		t.Fatal(err)
	}
	members, err := lists.MembersAmong(ctx, tenant, list.ID, []uuid.UUID{ana.ID, sinConsentimiento.ID, baja.ID, ajeno.ID})
	if err != nil {
		t.Fatal(err)
	}
	want := map[uuid.UUID]bool{ana.ID: true, sinConsentimiento.ID: true}
	if len(members) != 2 || !want[members[0]] || !want[members[1]] {
		t.Fatalf("miembros de la empresa, enviables o no; nunca el contacto ajeno: %v", members)
	}
	if other, err := lists.MembersAmong(ctx, otra, list.ID, []uuid.UUID{ana.ID}); err != nil || len(other) != 0 {
		t.Fatalf("otra empresa no ve la lista: %v %v", other, err)
	}
}
