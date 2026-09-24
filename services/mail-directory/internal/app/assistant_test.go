package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
)

type fakeAssistantSettings struct {
	rows map[uuid.UUID]*domain.AssistantSettings
}

func (f *fakeAssistantSettings) Get(_ context.Context, tenantID uuid.UUID) (*domain.AssistantSettings, error) {
	s, ok := f.rows[tenantID]
	if !ok {
		return nil, domain.ErrNotFound
	}
	c := *s
	return &c, nil
}

func (f *fakeAssistantSettings) Upsert(_ context.Context, s *domain.AssistantSettings) error {
	c := *s
	f.rows[s.TenantID] = &c
	return nil
}

func assistantHarness() (*harness, *fakeAssistantSettings) {
	h := newHarness()
	repo := &fakeAssistantSettings{rows: map[uuid.UUID]*domain.AssistantSettings{}}
	h.uc.assistant = repo
	return h, repo
}

func TestAsistenteActivarConservaElInstanteDeAceptacion(t *testing.T) {
	h, _ := assistantHarness()
	ctx := context.Background()
	tenant, admin := uuid.New(), uuid.New()

	s, err := h.uc.SetAssistantSettings(ctx, tenant, admin, true)
	if err != nil || !s.Enabled || s.EnabledAt == nil || !s.EnabledAt.Equal(h.clock) || *s.UpdatedBy != admin {
		t.Fatalf("activar: %+v %v", s, err)
	}
	first := *s.EnabledAt

	h.clock = h.clock.Add(time.Hour)
	s, err = h.uc.SetAssistantSettings(ctx, tenant, admin, true)
	if err != nil || !s.EnabledAt.Equal(first) {
		t.Fatalf("volver a guardar activado no mueve la aceptacion: %+v %v", s, err)
	}

	s, err = h.uc.SetAssistantSettings(ctx, tenant, admin, false)
	if err != nil || s.Enabled {
		t.Fatalf("apagar: %+v %v", s, err)
	}
	h.clock = h.clock.Add(time.Hour)
	s, err = h.uc.SetAssistantSettings(ctx, tenant, admin, true)
	if err != nil || !s.EnabledAt.Equal(h.clock) {
		t.Fatalf("reactivar es una aceptacion nueva: %+v %v", s, err)
	}
}

func TestAsistenteNoSeCambiaEnUnaEmpresaDeBaja(t *testing.T) {
	h, repo := assistantHarness()
	tenant := uuid.New()
	h.retirements.retired[tenant] = h.clock
	if _, err := h.uc.SetAssistantSettings(context.Background(), tenant, uuid.New(), true); !errors.Is(err, domain.ErrTenantRetired) {
		t.Fatalf("una empresa dada de baja no lo activa: %v", err)
	}
	if len(repo.rows) != 0 {
		t.Fatal("no debe guardar nada")
	}
}

func TestAsistentePorBuzonUsaLaEmpresaDelDirectorio(t *testing.T) {
	h, _ := assistantHarness()
	ctx := context.Background()
	tenant, other := uuid.New(), uuid.New()
	h.addMailbox(tenant, "ana@acme.test", 0)
	if _, err := h.uc.SetAssistantSettings(ctx, other, uuid.New(), true); err != nil {
		t.Fatal(err)
	}
	s, err := h.uc.AssistantSettingsByUsername(ctx, "Ana@Acme.test")
	if err != nil || s.Enabled || s.TenantID != tenant {
		t.Fatalf("la activacion de otra empresa no alcanza a este buzon: %+v %v", s, err)
	}
	if _, err := h.uc.SetAssistantSettings(ctx, tenant, uuid.New(), true); err != nil {
		t.Fatal(err)
	}
	if s, err = h.uc.AssistantSettingsByUsername(ctx, "ana@acme.test"); err != nil || !s.Enabled {
		t.Fatalf("activado para su empresa: %+v %v", s, err)
	}
}

func TestAsistenteSinRepositorioEsUnFallo(t *testing.T) {
	h := newHarness()
	if _, err := h.uc.AssistantSettings(context.Background(), uuid.New()); err == nil {
		t.Fatal("sin repositorio cableado debe fallar, no darlo por apagado en silencio")
	}
}
