package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/alonsosss/corforce-email/services/organization/internal/domain"
)

// PlatformTenantSlug es la empresa que crea ops/db/bootstrap-platform.sh fuera de la saga de alta.
const PlatformTenantSlug = "platform"

// EnsurePlatformDatabase crea y migra la base de la empresa de plataforma, la unica que nace fuera
// de la saga de alta: el alta de plataforma no puede llamar a la API (para eso hace falta ya un
// superadmin) y el barrido de migraciones solo migra bases que ya existen. Usa las mismas piezas
// que la saga: CreateDatabase la marca y la cierra a PUBLIC, y la adopta si ya lleva su marca;
// RunMigrations la migra con su registro. Devuelve true cuando la base queda lista; sin empresa de
// plataforma todavia no hay nada que hacer. No crea la base que falte a otra empresa: una base
// borrada por accidente no debe reaparecer vacia en silencio.
func (uc *OrganizationUseCase) EnsurePlatformDatabase(ctx context.Context) (bool, error) {
	t, err := uc.tenants.GetBySlug(ctx, PlatformTenantSlug)
	if errors.Is(err, domain.ErrTenantNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	target, err := uc.targetFor(ctx, t)
	if err != nil {
		return false, err
	}
	if err := uc.provisioner.CreateDatabase(ctx, target, t.ID); err != nil {
		return false, fmt.Errorf("crear la base de la empresa de plataforma: %w", err)
	}
	if err := uc.provisioner.RunMigrations(ctx, target); err != nil {
		return false, fmt.Errorf("migrar la base de la empresa de plataforma: %w", err)
	}
	return true, nil
}
