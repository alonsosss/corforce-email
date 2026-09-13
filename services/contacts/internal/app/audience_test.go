package app

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"testing"

	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/google/uuid"
)

func TestCursorDeAudiencia(t *testing.T) {
	id := uuid.New()
	cur := EncodeCursor(id)
	if len(cur) != 22 {
		t.Fatalf("un uuid en base64url sin relleno ocupa 22: %q", cur)
	}
	got, err := DecodeCursor(cur)
	if err != nil || got != id {
		t.Fatalf("ida y vuelta: %v %v", got, err)
	}
	if got, err := DecodeCursor(""); err != nil || got != uuid.Nil {
		t.Fatalf("vacio = primera pagina: %v %v", got, err)
	}
	for _, bad := range []string{
		"no base64!",
		base64.RawURLEncoding.EncodeToString([]byte("corto")),
		base64.RawURLEncoding.EncodeToString(make([]byte, 17)),
		base64.RawURLEncoding.EncodeToString(make([]byte, 16)),
		base64.StdEncoding.EncodeToString(id[:]),
	} {
		if _, err := DecodeCursor(bad); !errors.Is(err, domain.ErrInvalidCursor) {
			t.Errorf("%q: se esperaba ErrInvalidCursor, hubo %v", bad, err)
		}
	}
}

func TestAudienciaPaginaSinDuplicadosNiSaltos(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	list, err := f.uc.CreateList(ctx, f.tenant, "Clientes", "")
	if err != nil {
		t.Fatal(err)
	}
	var ids []uuid.UUID
	for i := 0; i < 5; i++ {
		c := f.addContact(t, fmt.Sprintf("c%d@example.com", i), domain.StatusActive, domain.ConsentGranted)
		ids = append(ids, c.ID)
	}
	noEnviable := f.addContact(t, "baja@example.com", domain.StatusUnsubscribed, domain.ConsentRevoked)
	ids = append(ids, noEnviable.ID)
	if _, err := f.uc.AddMembers(ctx, f.tenant, list.ID, ids); err != nil {
		t.Fatal(err)
	}

	seen := map[uuid.UUID]bool{}
	cursor := ""
	pages := 0
	for {
		page, err := f.uc.Audience(ctx, f.tenant, AudienceInput{ListIDs: []uuid.UUID{list.ID, list.ID}, Cursor: cursor, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		pages++
		if f.query.lastSpec.Limit != 3 || len(f.query.lastSpec.ListIDs) != 1 {
			t.Fatalf("se pide uno de mas y sin ids repetidos: %+v", f.query.lastSpec)
		}
		for _, c := range page.Contacts {
			if seen[c.ID] {
				t.Fatalf("contacto repetido entre paginas: %s", c.Email)
			}
			seen[c.ID] = true
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	if pages != 3 || len(seen) != 5 || seen[noEnviable.ID] {
		t.Fatalf("paginas=%d contactos=%d", pages, len(seen))
	}
}

func TestAudienciaValida(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	list, _ := f.uc.CreateList(ctx, f.tenant, "L", "")
	cases := []struct {
		in   AudienceInput
		want error
	}{
		{AudienceInput{}, domain.ErrInvalidAudience},
		{AudienceInput{ExcludeSegmentIDs: []uuid.UUID{uuid.New()}}, domain.ErrInvalidAudience},
		{AudienceInput{ListIDs: []uuid.UUID{list.ID}, Limit: 1001}, domain.ErrInvalidLimit},
		{AudienceInput{ListIDs: []uuid.UUID{list.ID}, Limit: -1}, domain.ErrInvalidLimit},
		{AudienceInput{ListIDs: []uuid.UUID{list.ID}, Cursor: "xx"}, domain.ErrInvalidCursor},
		{AudienceInput{ListIDs: []uuid.UUID{uuid.New()}}, domain.ErrListNotFound},
		{AudienceInput{SegmentIDs: []uuid.UUID{uuid.New()}}, domain.ErrSegmentNotFound},
		{AudienceInput{ListIDs: []uuid.UUID{list.ID}, ExcludeSegmentIDs: []uuid.UUID{uuid.New()}}, domain.ErrSegmentNotFound},
	}
	for _, tc := range cases {
		if _, err := f.uc.Audience(ctx, f.tenant, tc.in); !errors.Is(err, tc.want) {
			t.Errorf("%+v: se esperaba %v, hubo %v", tc.in, tc.want, err)
		}
	}
	many := make([]uuid.UUID, MaxAudienceRefs+1)
	for i := range many {
		many[i] = uuid.New()
	}
	if _, err := f.uc.Audience(ctx, f.tenant, AudienceInput{ListIDs: many}); !errors.Is(err, domain.ErrInvalidAudience) {
		t.Fatalf("demasiadas listas: %v", err)
	}

	page, err := f.uc.Audience(ctx, f.tenant, AudienceInput{ListIDs: []uuid.UUID{list.ID}})
	if err != nil || page.Contacts == nil || page.NextCursor != nil || f.query.lastSpec.Limit != MaxAudienceLimit+1 {
		t.Fatalf("pagina vacia con el limite por defecto: %+v %v", page, err)
	}
}
