package app

import (
	"context"
	"errors"
	"testing"

	"github.com/alonsosss/corforce-email/services/organization/internal/domain"
	"github.com/google/uuid"
)

func TestTenantCellDevuelveLaCeldaDeLaEmpresa(t *testing.T) {
	cells := cellsFixture()
	enCelda := &domain.Tenant{ID: uuid.New(), Slug: "acme", CellID: cells[0].ID, Status: domain.TenantStatusSuspended}
	sinCelda := &domain.Tenant{ID: uuid.New(), Slug: "rota", CellID: uuid.New(), Status: domain.TenantStatusActive}
	uc := NewOrganizationUseCase(Dependencies{
		Tenants: &fakeTenantRepo{tenants: []*domain.Tenant{enCelda, sinCelda}},
		Cells:   &fakeCellRepo{cells: cells},
	})
	ctx := context.Background()

	// El estado de la empresa no cambia su celda: una suspendida sigue viviendo en la suya.
	cell, err := uc.TenantCell(ctx, enCelda.ID)
	if err != nil || cell.Code != "pe-01" {
		t.Fatalf("celda de acme: %+v %v", cell, err)
	}
	if _, err := uc.TenantCell(ctx, uuid.New()); !errors.Is(err, domain.ErrTenantNotFound) {
		t.Fatalf("empresa inexistente: %v, se esperaba ErrTenantNotFound", err)
	}
	// Un registro incoherente no se confunde con una empresa que no existe.
	_, err = uc.TenantCell(ctx, sinCelda.ID)
	if !errors.Is(err, domain.ErrCellNotFound) || errors.Is(err, domain.ErrTenantNotFound) {
		t.Fatalf("celda que falta en el directorio: %v", err)
	}
}
