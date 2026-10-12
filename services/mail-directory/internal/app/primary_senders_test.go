package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/ports"
	"github.com/google/uuid"
)

type fakePrimarySenders struct {
	items map[uuid.UUID]*domain.PrimarySender
	now   func() time.Time
}

var _ ports.PrimarySenderRepository = (*fakePrimarySenders)(nil)

func (f *fakePrimarySenders) ListByUsername(_ context.Context, tenantID uuid.UUID, username string) ([]domain.PrimarySender, error) {
	var out []domain.PrimarySender
	for _, s := range f.items {
		if s.TenantID == tenantID && s.Username == username {
			out = append(out, *s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Address < out[j].Address })
	return out, nil
}

func (f *fakePrimarySenders) Count(ctx context.Context, tenantID uuid.UUID, username string) (int, error) {
	items, _ := f.ListByUsername(ctx, tenantID, username)
	return len(items), nil
}

func (f *fakePrimarySenders) Add(_ context.Context, s *domain.PrimarySender) error {
	if f.items == nil {
		f.items = map[uuid.UUID]*domain.PrimarySender{}
	}
	for _, other := range f.items {
		if other.TenantID == s.TenantID && other.Username == s.Username && other.Address == s.Address {
			*s = *other
			return nil
		}
	}
	s.CreatedAt = f.now()
	c := *s
	f.items[s.ID] = &c
	return nil
}

func (f *fakePrimarySenders) Remove(_ context.Context, tenantID uuid.UUID, username, address string) error {
	for id, s := range f.items {
		if s.TenantID == tenantID && s.Username == username && s.Address == address {
			delete(f.items, id)
		}
	}
	return nil
}

func (f *fakePrimarySenders) DeleteByUsername(_ context.Context, tenantID uuid.UUID, username string) error {
	for id, s := range f.items {
		if s.TenantID == tenantID && s.Username == username {
			delete(f.items, id)
		}
	}
	return nil
}

func TestRemitentesFijadosEnPrincipal(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	h.addMailbox(tenant, "ana@acme.test", 0)
	h.addMailbox(uuid.New(), "luis@otra.test", 0)
	ctx := context.Background()

	s, err := h.uc.AddPrimarySender(ctx, "Ana@acme.test", "  Soporte@Proveedor.EXAMPLE ")
	if err != nil || s.Address != "soporte@proveedor.example" || s.TenantID != tenant || s.Username != "ana@acme.test" {
		t.Fatalf("alta: %+v %v", s, err)
	}
	again, err := h.uc.AddPrimarySender(ctx, "ana@acme.test", "soporte@proveedor.example")
	if err != nil || again.ID != s.ID {
		t.Fatalf("repetir es idempotente: %+v %v", again, err)
	}
	if _, err := h.uc.AddPrimarySender(ctx, "ana@acme.test", "banco@otro.example"); err != nil {
		t.Fatal(err)
	}
	list, err := h.uc.PrimarySenders(ctx, "ana@acme.test")
	if err != nil || len(list) != 2 || list[0].Address != "banco@otro.example" {
		t.Fatalf("listado: %+v %v", list, err)
	}
	if other, err := h.uc.PrimarySenders(ctx, "luis@otra.test"); err != nil || len(other) != 0 {
		t.Fatalf("otro buzon no ve los de Ana: %+v %v", other, err)
	}

	if err := h.uc.RemovePrimarySender(ctx, "ana@acme.test", "SOPORTE@proveedor.example"); err != nil {
		t.Fatal(err)
	}
	if err := h.uc.RemovePrimarySender(ctx, "ana@acme.test", "soporte@proveedor.example"); err != nil {
		t.Fatalf("quitar una que no estaba no es un error: %v", err)
	}
	if list, _ := h.uc.PrimarySenders(ctx, "ana@acme.test"); len(list) != 1 {
		t.Fatalf("queda uno: %+v", list)
	}
}

func TestRemitenteFijadoRechazaDireccionesInvalidas(t *testing.T) {
	h := newHarness()
	h.addMailbox(uuid.New(), "ana@acme.test", 0)
	for _, bad := range []string{"", "sin-arroba", "Nombre <a@b.example>", "a@b.example, c@d.example", "@b.example"} {
		if _, err := h.uc.AddPrimarySender(context.Background(), "ana@acme.test", bad); err == nil {
			t.Errorf("%q: debe rechazarse", bad)
		}
	}
}

func TestRemitentesFijadosTienenTopePorBuzon(t *testing.T) {
	h := newHarness()
	h.addMailbox(uuid.New(), "ana@acme.test", 0)
	ctx := context.Background()
	for i := 0; i < domain.MaxPrimarySenders; i++ {
		if _, err := h.uc.AddPrimarySender(ctx, "ana@acme.test", fmt.Sprintf("p%d@x.example", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.uc.AddPrimarySender(ctx, "ana@acme.test", "uno-mas@x.example"); !errors.Is(err, domain.ErrPrimarySenderLimit) {
		t.Fatalf("tope: %v", err)
	}
	if _, err := h.uc.AddPrimarySender(ctx, "ana@acme.test", "p0@x.example"); err != nil {
		t.Fatalf("una ya fijada no cuenta contra el tope: %v", err)
	}
}

func TestBorrarUnBuzonBorraSusRemitentesFijados(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	mb := h.addMailbox(tenant, "ana@acme.test", 0)
	ctx := context.Background()
	if _, err := h.uc.AddPrimarySender(ctx, "ana@acme.test", "soporte@proveedor.example"); err != nil {
		t.Fatal(err)
	}
	if err := h.uc.DeleteMailbox(ctx, tenant, mb.ID); err != nil {
		t.Fatal(err)
	}
	if n := len(h.primarySenders.items); n != 0 {
		t.Fatalf("quedaron %d filas", n)
	}
}
