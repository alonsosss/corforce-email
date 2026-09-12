package app

import (
	"context"
	"errors"
	"testing"

	"github.com/alonsosss/corforce-email/services/organization/internal/domain"
	"github.com/google/uuid"
)

func TestClosureRequires(t *testing.T) {
	got := closureRequires("marketing", reqMapFixture())
	for _, want := range []string{"marketing", "transactional"} {
		if !got[want] {
			t.Errorf("closureRequires(marketing) debe incluir %q; got %v", want, got)
		}
	}
	if got["corporate_mail"] {
		t.Errorf("closureRequires(marketing) no debe incluir corporate_mail; got %v", got)
	}
}

func TestRequiresTransitively(t *testing.T) {
	rm := reqMapFixture()
	cases := []struct {
		module, target string
		want           bool
	}{
		{"marketing", "transactional", true},
		{"marketing", "corporate_mail", false},
		{"transactional", "marketing", false},
		{"corporate_mail", "transactional", false},
	}
	for _, c := range cases {
		if got := requiresTransitively(c.module, c.target, rm); got != c.want {
			t.Errorf("requiresTransitively(%s,%s)=%v, want %v", c.module, c.target, got, c.want)
		}
	}
}

// Sin filas propias todo esta habilitado; con filas, lo ausente cuenta como apagado.
func TestEffectiveModules(t *testing.T) {
	catalog := catalogFixture()
	all := effectiveModules(catalog, nil)
	for _, m := range all {
		if !m.Enabled {
			t.Errorf("sin catalogo explicito, %q deberia estar habilitado", m.Module)
		}
	}
	partial := effectiveModules(catalog, []domain.TenantModuleState{{Module: "corporate_mail", Enabled: true}})
	for _, m := range partial {
		if want := m.Module == "corporate_mail"; m.Enabled != want {
			t.Errorf("modulo %q: enabled=%v, want %v", m.Module, m.Enabled, want)
		}
	}
}

// PermissionModules viaja del catalogo a la respuesta: access-control y el gateway lo
// leen de aqui y no de una lista en codigo.
func TestGetTenantModulesExponePermissionModules(t *testing.T) {
	tenant := &domain.Tenant{ID: uuid.New(), Slug: "acme", Status: domain.TenantStatusActive}
	uc := NewOrganizationUseCase(Dependencies{
		Tenants: &fakeTenantRepo{tenants: []*domain.Tenant{tenant}},
		Modules: &fakeModulesRepo{catalog: catalogFixture()},
	})
	got, err := uc.GetTenantModules(context.Background(), tenant.ID)
	if err != nil {
		t.Fatalf("GetTenantModules: %v", err)
	}
	for _, m := range got {
		if len(m.PermissionModules) == 0 {
			t.Errorf("modulo %q sin permission_modules", m.Module)
		}
	}
}

func TestSetTenantModuleRespetaDependenciasYPublica(t *testing.T) {
	tenant := &domain.Tenant{ID: uuid.New(), Slug: "acme", Status: domain.TenantStatusActive}
	modules := &fakeModulesRepo{catalog: catalogFixture()}
	pub := &fakePublisher{}
	uc := NewOrganizationUseCase(Dependencies{
		Tenants:   &fakeTenantRepo{tenants: []*domain.Tenant{tenant}},
		Modules:   modules,
		Publisher: pub,
	})
	ctx := context.Background()

	// Apagar transactional con marketing habilitado (linea base = todo) se rechaza.
	if err := uc.SetTenantModule(ctx, tenant.ID, "transactional", false); !errors.Is(err, domain.ErrModuleRequired) {
		t.Fatalf("apagar una dependencia en uso = %v; want ErrModuleRequired", err)
	}
	// La linea base quedo sembrada con todo habilitado.
	if len(modules.states[tenant.ID]) != len(catalogFixture()) {
		t.Fatalf("la linea base debe cubrir todo el catalogo; got %v", modules.states[tenant.ID])
	}

	if err := uc.SetTenantModule(ctx, tenant.ID, "marketing", false); err != nil {
		t.Fatalf("apagar marketing: %v", err)
	}
	if err := uc.SetTenantModule(ctx, tenant.ID, "transactional", false); err != nil {
		t.Fatalf("apagar transactional sin dependientes: %v", err)
	}
	// Encender marketing arrastra transactional.
	if err := uc.SetTenantModule(ctx, tenant.ID, "marketing", true); err != nil {
		t.Fatalf("encender marketing: %v", err)
	}
	if !modules.states[tenant.ID]["transactional"] {
		t.Error("encender marketing debe habilitar transactional")
	}
	if len(pub.modules) != 3 {
		t.Errorf("se publicaron %d eventos de modulos; want 3 (uno por cambio aplicado)", len(pub.modules))
	}
	last := pub.modules[len(pub.modules)-1]
	if len(last) != 3 {
		t.Errorf("el ultimo evento debe llevar los tres modulos habilitados; got %v", last)
	}

	if err := uc.SetTenantModule(ctx, tenant.ID, "inexistente", true); !errors.Is(err, domain.ErrModuleNotFound) {
		t.Errorf("modulo desconocido = %v; want ErrModuleNotFound", err)
	}
	if err := uc.SetTenantModule(ctx, uuid.New(), "marketing", true); !errors.Is(err, domain.ErrTenantNotFound) {
		t.Errorf("tenant desconocido = %v; want ErrTenantNotFound", err)
	}
}
