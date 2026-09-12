package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/alonsosss/corforce-email/services/access-control/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TenantModuleGateRepo lee el catalogo de modulos contratables y su estado por tenant
// (schema organization, misma base de registro que access_control).
type TenantModuleGateRepo struct {
	pool *pgxpool.Pool
}

func NewTenantModuleGateRepo(pool *pgxpool.Pool) *TenantModuleGateRepo {
	return &TenantModuleGateRepo{pool: pool}
}

// EffectiveModules traduce el catalogo al vocabulario de los permisos. Cada modulo del
// catalogo declara en permission_modules (jsonb, lista de cadenas) que modulos de permiso
// cubre; si no declara ninguno se entiende que su propio nombre es el modulo de permiso.
// Un modulo del catalogo esta habilitado si es core o si el tenant lo tiene encendido;
// con estado explicito, lo que no esta encendido esta apagado.
func (r *TenantModuleGateRepo) EffectiveModules(ctx context.Context, tenantID uuid.UUID) (domain.ModuleAvailability, error) {
	var restricted bool
	if err := r.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM organization.tenant_modules WHERE tenant_id = $1)`,
		tenantID).Scan(&restricted); err != nil {
		return domain.ModuleAvailability{}, err
	}
	if !restricted {
		return domain.ModuleAvailability{}, nil
	}

	rows, err := r.pool.Query(ctx,
		`SELECT mc.module,
		        mc.tier = 'core' OR COALESCE(tm.enabled, false) AS enabled,
		        COALESCE(mc.permission_modules, '[]'::jsonb)
		   FROM organization.module_catalog mc
		   LEFT JOIN organization.tenant_modules tm
		     ON tm.module = mc.module AND tm.tenant_id = $1`, tenantID)
	if err != nil {
		return domain.ModuleAvailability{}, err
	}
	defer rows.Close()

	gated := map[string]bool{}
	for rows.Next() {
		var module string
		var enabled bool
		var raw []byte
		if err := rows.Scan(&module, &enabled, &raw); err != nil {
			return domain.ModuleAvailability{}, err
		}
		var permissionModules []string
		if err := json.Unmarshal(raw, &permissionModules); err != nil {
			return domain.ModuleAvailability{}, fmt.Errorf("permission_modules de %s: %w", module, err)
		}
		if len(permissionModules) == 0 {
			permissionModules = []string{module}
		}
		// Un modulo de permiso reclamado por varios modulos del catalogo basta con que uno
		// lo habilite.
		for _, pm := range permissionModules {
			gated[pm] = gated[pm] || enabled
		}
	}
	if err := rows.Err(); err != nil {
		return domain.ModuleAvailability{}, err
	}
	return domain.ModuleAvailability{Restricted: true, Gated: gated}, nil
}
