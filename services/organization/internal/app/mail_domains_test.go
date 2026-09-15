package app

import (
	"context"
	"errors"
	"testing"

	"github.com/alonsosss/corforce-email/services/organization/internal/domain"
	"github.com/google/uuid"
)

// fakeMailDomains es el indice de dominios en memoria, con la semantica del repositorio real.
type fakeMailDomains struct {
	owners map[string]uuid.UUID
	// log anota las retiradas de una empresa entera en orden con los demas pasos de la saga.
	log *callLog
	// removing dice si la empresa tiene la baja en curso, lo que el repositorio real consulta en
	// la misma sentencia del reclamo.
	removing func(tenantID uuid.UUID) bool
}

func (f *fakeMailDomains) Claim(_ context.Context, name string, tenantID uuid.UUID) error {
	if f.removing != nil && f.removing(tenantID) {
		return domain.ErrTenantBeingRemoved
	}
	if owner, ok := f.owners[name]; ok && owner != tenantID {
		return domain.ErrMailDomainClaimed
	}
	f.owners[name] = tenantID
	return nil
}

func (f *fakeMailDomains) Release(_ context.Context, name string, tenantID uuid.UUID) (bool, error) {
	if owner, ok := f.owners[name]; ok && owner == tenantID {
		delete(f.owners, name)
		return true, nil
	}
	return false, nil
}

func (f *fakeMailDomains) ReleaseTenant(_ context.Context, tenantID uuid.UUID) (int64, error) {
	if f.log != nil {
		if err := f.log.record("index.release"); err != nil {
			return 0, err
		}
	}
	var released int64
	for name, owner := range f.owners {
		if owner == tenantID {
			delete(f.owners, name)
			released++
		}
	}
	return released, nil
}

func (f *fakeMailDomains) TenantOf(_ context.Context, name string) (uuid.UUID, error) {
	owner, ok := f.owners[name]
	if !ok {
		return uuid.Nil, domain.ErrMailDomainNotFound
	}
	return owner, nil
}

func TestIndiceDeDominiosDeCorreo(t *testing.T) {
	pe01 := &domain.Cell{ID: uuid.New(), Code: "pe-01"}
	pe02 := &domain.Cell{ID: uuid.New(), Code: "pe-02"}
	acme := &domain.Tenant{ID: uuid.New(), Slug: "acme", CellID: pe01.ID, Status: domain.TenantStatusActive}
	beta := &domain.Tenant{ID: uuid.New(), Slug: "beta", CellID: pe02.ID, Status: domain.TenantStatusActive}
	index := &fakeMailDomains{owners: map[string]uuid.UUID{}}
	uc := NewOrganizationUseCase(Dependencies{
		Tenants:     &fakeTenantRepo{tenants: []*domain.Tenant{acme, beta}},
		Cells:       &fakeCellRepo{cells: []*domain.Cell{pe01, pe02}},
		MailDomains: index,
	})
	ctx := context.Background()

	name, cell, err := uc.ClaimMailDomain(ctx, beta.ID, " Beta.TEST ")
	if err != nil || name != "beta.test" || cell.Code != "pe-02" {
		t.Fatalf("reclamar beta.test para beta: %q %+v %v", name, cell, err)
	}
	if _, _, err := uc.ClaimMailDomain(ctx, beta.ID, "beta.test"); err != nil {
		t.Fatalf("reclamarlo otra vez es idempotente: %v", err)
	}
	// Un dominio activo no se activa en otra empresa, tampoco de otra celda.
	if _, _, err := uc.ClaimMailDomain(ctx, acme.ID, "beta.test"); !errors.Is(err, domain.ErrMailDomainClaimed) {
		t.Fatalf("otra empresa reclama beta.test: %v", err)
	}
	if _, _, err := uc.ClaimMailDomain(ctx, uuid.New(), "gamma.test"); !errors.Is(err, domain.ErrTenantNotFound) {
		t.Fatalf("empresa desconocida: %v", err)
	}
	if _, _, err := uc.ClaimMailDomain(ctx, acme.ID, "no-es-un-dominio"); !errors.Is(err, domain.ErrInvalidMailDomain) {
		t.Fatalf("nombre invalido: %v", err)
	}
	if len(index.owners) != 1 || index.owners["beta.test"] != beta.ID {
		t.Fatalf("indice: %v", index.owners)
	}

	name, cell, err = uc.MailDomainCell(ctx, "BETA.test")
	if err != nil || name != "beta.test" || cell.Code != "pe-02" {
		t.Fatalf("celda de beta.test: %q %+v %v", name, cell, err)
	}
	for _, raw := range []string{"nadie.test", "no-es-un-dominio", ""} {
		if _, _, err := uc.MailDomainCell(ctx, raw); !errors.Is(err, domain.ErrMailDomainNotFound) {
			t.Fatalf("%q: %v", raw, err)
		}
	}

	// Soltar lo que no es suyo no toca nada; el dueno lo suelta y soltarlo otra vez no falla.
	if err := uc.ReleaseMailDomain(ctx, acme.ID, "beta.test"); err != nil || index.owners["beta.test"] != beta.ID {
		t.Fatalf("acme suelta beta.test: %v %v", err, index.owners)
	}
	for i := 0; i < 2; i++ {
		if err := uc.ReleaseMailDomain(ctx, beta.ID, "beta.test"); err != nil {
			t.Fatalf("beta suelta beta.test (%d): %v", i, err)
		}
	}
	if _, _, err := uc.MailDomainCell(ctx, "beta.test"); !errors.Is(err, domain.ErrMailDomainNotFound) {
		t.Fatalf("dominio soltado: %v", err)
	}
	if err := uc.ReleaseMailDomain(ctx, beta.ID, "no_es"); !errors.Is(err, domain.ErrInvalidMailDomain) {
		t.Fatalf("soltar un nombre invalido: %v", err)
	}

	// Un dominio cuya empresa falta es un registro incoherente, no un dominio desconocido.
	index.owners["huerfano.test"] = uuid.New()
	_, _, err = uc.MailDomainCell(ctx, "huerfano.test")
	if err == nil || errors.Is(err, domain.ErrMailDomainNotFound) || errors.Is(err, domain.ErrTenantNotFound) {
		t.Fatalf("empresa que falta: %v", err)
	}
}
