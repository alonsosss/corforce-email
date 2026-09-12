package ports

import (
	"context"

	"github.com/alonsosss/corforce-email/services/access-control/internal/domain"
	"github.com/google/uuid"
)

// TenantModuleGate expone que modulos tiene contratados cada tenant (schema organization
// de la base de registro), para acotar los modulos que el RBAC reporta. Vive en un unico
// punto (GetUserAccess) que alimenta el menu del shell y el gateway.
type TenantModuleGate interface {
	// EffectiveModules devuelve Restricted=false cuando el tenant no tiene estado
	// explicito en organization.tenant_modules (todo habilitado). Con Restricted=true,
	// Gated contiene los modulos de permiso reclamados por el catalogo y si estan
	// habilitados; los modulos core del catalogo cuentan siempre como habilitados.
	EffectiveModules(ctx context.Context, tenantID uuid.UUID) (domain.ModuleAvailability, error)
}
