package app

import (
	"context"
	"errors"
	"testing"

	"github.com/alonsosss/corforce-email/services/organization/internal/domain"
	"github.com/google/uuid"
)

func conPlataforma() *fakeTenantRepo {
	return &fakeTenantRepo{tenants: append(tenantsFixture(),
		&domain.Tenant{ID: uuid.New(), Slug: PlatformTenantSlug, DBName: "mail_tenant_platform", Status: domain.TenantStatusActive})}
}

func TestEnsurePlatformDatabaseSinEmpresaDePlataformaNoTocaNada(t *testing.T) {
	prov := &fakeProvisioner{}
	listo, err := newMigrationsUC(&fakeTenantRepo{tenants: tenantsFixture()}, prov).EnsurePlatformDatabase(context.Background())
	if err != nil || listo {
		t.Fatalf("sin empresa de plataforma: listo=%v err=%v", listo, err)
	}
	if len(prov.created) != 0 || len(prov.ran) != 0 {
		t.Fatalf("creo %v y migro %v sin empresa de plataforma", prov.created, prov.ran)
	}
}

func TestEnsurePlatformDatabaseCreaYMigraSoloLaDePlataforma(t *testing.T) {
	prov := &fakeProvisioner{}
	listo, err := newMigrationsUC(conPlataforma(), prov).EnsurePlatformDatabase(context.Background())
	if err != nil || !listo {
		t.Fatalf("listo=%v err=%v", listo, err)
	}
	if len(prov.created) != 1 || prov.created[0] != "mail_tenant_platform" {
		t.Fatalf("bases creadas: %v; se esperaba solo mail_tenant_platform", prov.created)
	}
	if len(prov.ran) != 1 || prov.ran[0] != "mail_tenant_platform" {
		t.Fatalf("bases migradas: %v; se esperaba solo mail_tenant_platform", prov.ran)
	}
}

func TestEnsurePlatformDatabaseEsIdempotente(t *testing.T) {
	repo, prov := conPlataforma(), &fakeProvisioner{}
	uc := newMigrationsUC(repo, prov)
	for i := range 2 {
		if listo, err := uc.EnsurePlatformDatabase(context.Background()); err != nil || !listo {
			t.Fatalf("pasada %d: listo=%v err=%v", i+1, listo, err)
		}
	}
}

func TestEnsurePlatformDatabaseNoAdoptaUnaBaseAjenaNiLaMigra(t *testing.T) {
	prov := &fakeProvisioner{owner: map[string]uuid.UUID{"mail_tenant_platform": uuid.New()}}
	listo, err := newMigrationsUC(conPlataforma(), prov).EnsurePlatformDatabase(context.Background())
	if listo || !errors.Is(err, domain.ErrDatabaseOccupied) {
		t.Fatalf("base ajena: listo=%v err=%v; se esperaba ErrDatabaseOccupied", listo, err)
	}
	if len(prov.ran) != 0 {
		t.Fatalf("migro una base ajena: %v", prov.ran)
	}
}

func TestEnsurePlatformDatabaseConLasMigracionesTomadasNoQuedaLista(t *testing.T) {
	prov := &fakeProvisioner{runErr: map[string]error{"mail_tenant_platform": domain.ErrMigrationsLocked}}
	listo, err := newMigrationsUC(conPlataforma(), prov).EnsurePlatformDatabase(context.Background())
	if listo || !errors.Is(err, domain.ErrMigrationsLocked) {
		t.Fatalf("migraciones tomadas por otra replica: listo=%v err=%v", listo, err)
	}
}
