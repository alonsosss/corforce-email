//go:build integration

package postgres

import (
	"bytes"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/alonsosss/corforce-email/services/contacts/internal/ports"
	"github.com/alonsosss/corforce-email/services/contacts/internal/segment"
	"github.com/google/uuid"
)

// Los estados invalid y excluded contra la base real: el CHECK de 02_status_exclusions.sql
// los admite y sigue rechazando lo que no es un estado; la regla de enviable los deja
// fuera de la audiencia y de la consulta por id aunque tengan consentimiento; el listado y
// los segmentos filtran por ellos; y el recorrido del barrido pagina por id, con y sin
// estado, sin salir de la empresa.
func TestEstadosDeExclusionEnLaBase(t *testing.T) {
	_, ctx := testPool(t)
	cp := &db.ContextPool{}
	contacts := NewContactRepository(cp)
	consents := NewConsentRepository(cp)
	lists := NewListRepository(cp)
	query := NewSegmentQuery(cp)
	tenant := uuid.New()

	byStatus := map[domain.Status]*domain.Contact{}
	var ids []uuid.UUID
	for _, st := range domain.Statuses() {
		c := insert(t, ctx, contacts, newContact(tenant, "estado-"+string(st)+"@example.com"))
		if err := consents.Append(ctx, &domain.Consent{TenantID: tenant, ContactID: c.ID, Purpose: domain.PurposeMarketing,
			Status: domain.ConsentGranted, Method: domain.MethodForm, Source: "https://example.com/alta", IP: strp("203.0.113.9")}); err != nil {
			t.Fatal(err)
		}
		c.Status = st
		if err := contacts.Update(ctx, c); err != nil {
			t.Fatalf("el CHECK admite %s: %v", st, err)
		}
		byStatus[st] = c
		ids = append(ids, c.ID)
	}
	ajeno := insert(t, ctx, contacts, newContact(uuid.New(), "estado-ajeno@example.com"))
	ajeno.Status = domain.StatusExcluded
	if err := contacts.Update(ctx, ajeno); err != nil {
		t.Fatal(err)
	}

	bogus := *byStatus[domain.StatusActive]
	bogus.Status = "suppressed"
	if err := contacts.Update(ctx, &bogus); sqlState(err) != "23514" {
		t.Fatalf("un estado que no existe lo rechaza el CHECK: %v", err)
	}

	active := byStatus[domain.StatusActive]
	sendable, err := query.Sendable(ctx, tenant, ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(sendable) != 1 || sendable[0].ID != active.ID {
		t.Fatalf("solo el activo es enviable: %+v", sendable)
	}

	list := &domain.List{TenantID: tenant, Name: "Estados de exclusion"}
	if err := lists.Create(ctx, list); err != nil {
		t.Fatal(err)
	}
	if _, err := lists.AddMembers(ctx, tenant, list.ID, ids); err != nil {
		t.Fatal(err)
	}
	audience, err := query.Audience(ctx, tenant, ports.AudienceSpec{ListIDs: []uuid.UUID{list.ID}, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(audience) != 1 || audience[0].ID != active.ID {
		t.Fatalf("la audiencia de la lista solo trae al activo: %+v", audience)
	}

	statuses := make([]string, 0, len(domain.Statuses()))
	for _, s := range domain.Statuses() {
		statuses = append(statuses, string(s))
	}
	schema := segment.Schema{Enums: map[string][]string{"status": statuses}}
	def, err := segment.Parse([]byte(`{"match":"all","rules":[{"field":"status","op":"in","value":["invalid","excluded"]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if n, err := query.Count(ctx, tenant, def, schema); err != nil || n != 2 {
		t.Fatalf("el segmento cuenta los dos estados nuevos de la empresa: %d %v", n, err)
	}
	notActive, err := segment.Parse([]byte(`{"match":"all","rules":[{"field":"status","op":"neq","value":"active"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if n, err := query.Count(ctx, tenant, notActive, schema); err != nil || n != int64(len(statuses)-1) {
		t.Fatalf("todos menos el activo: %d %v", n, err)
	}

	listed, total, err := contacts.List(ctx, tenant, ports.ContactFilter{Status: domain.StatusExcluded, Page: 1, PerPage: 10})
	if err != nil || total != 1 || len(listed) != 1 || listed[0].ID != byStatus[domain.StatusExcluded].ID {
		t.Fatalf("el listado filtra por excluded: %+v %d %v", listed, total, err)
	}

	only, err := contacts.ListAfter(ctx, tenant, domain.StatusExcluded, uuid.Nil, 10)
	if err != nil || len(only) != 1 || only[0].ID != byStatus[domain.StatusExcluded].ID {
		t.Fatalf("el recorrido por estado no sale de la empresa: %+v %v", only, err)
	}
	var walked []domain.Contact
	after := uuid.Nil
	for {
		page, err := contacts.ListAfter(ctx, tenant, "", after, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		walked = append(walked, page...)
		after = page[len(page)-1].ID
	}
	if len(walked) != len(ids) {
		t.Fatalf("el recorrido completo ve %d contactos, se esperaban %d", len(walked), len(ids))
	}
	for i := 1; i < len(walked); i++ {
		if bytes.Compare(walked[i-1].ID[:], walked[i].ID[:]) >= 0 {
			t.Fatalf("el recorrido va en orden de id: %s antes que %s", walked[i-1].ID, walked[i].ID)
		}
	}
}
