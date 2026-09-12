package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/alonsosss/corforce-email/services/organization/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ModulesRepo accede al catalogo de modulos y al estado por tenant en el esquema
// organization de la base de registro.
type ModulesRepo struct {
	pool *pgxpool.Pool
}

func NewModulesRepo(pool *pgxpool.Pool) *ModulesRepo {
	return &ModulesRepo{pool: pool}
}

func (r *ModulesRepo) ListCatalog(ctx context.Context) ([]domain.ModuleCatalogEntry, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT module, tier, requires, label, permission_modules
		   FROM organization.module_catalog ORDER BY module`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.ModuleCatalogEntry
	for rows.Next() {
		var e domain.ModuleCatalogEntry
		var requiresRaw, permissionRaw []byte
		if err := rows.Scan(&e.Module, &e.Tier, &requiresRaw, &e.Label, &permissionRaw); err != nil {
			return nil, err
		}
		if err := decodeStringList(requiresRaw, &e.Requires); err != nil {
			return nil, fmt.Errorf("leer requires de %s: %w", e.Module, err)
		}
		if err := decodeStringList(permissionRaw, &e.PermissionModules); err != nil {
			return nil, fmt.Errorf("leer permission_modules de %s: %w", e.Module, err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// decodeStringList deja siempre una lista no nula: el JSON de salida debe ser [] y no
// null para que ningun consumidor tenga que distinguir ambos.
func decodeStringList(raw []byte, dst *[]string) error {
	*dst = []string{}
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, dst)
}

// HasAny indica si el tenant tiene un catalogo explicito. Falso => "todos habilitados".
func (r *ModulesRepo) HasAny(ctx context.Context, tenantID uuid.UUID) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM organization.tenant_modules WHERE tenant_id = $1)`,
		tenantID,
	).Scan(&exists)
	return exists, err
}

func (r *ModulesRepo) ListForTenant(ctx context.Context, tenantID uuid.UUID) ([]domain.TenantModuleState, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT module, enabled FROM organization.tenant_modules WHERE tenant_id = $1`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.TenantModuleState
	for rows.Next() {
		var s domain.TenantModuleState
		if err := rows.Scan(&s.Module, &s.Enabled); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// DeleteForTenant borra todas las filas de modulos del tenant, devolviendolo al estado
// "sin catalogo explicito" = todos los modulos habilitados.
func (r *ModulesRepo) DeleteForTenant(ctx context.Context, tenantID uuid.UUID) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM organization.tenant_modules WHERE tenant_id = $1`, tenantID)
	return err
}

const upsertTenantModule = `INSERT INTO organization.tenant_modules (tenant_id, module, enabled, updated_at)
 VALUES ($1, $2, $3, now())
 ON CONFLICT (tenant_id, module) DO UPDATE SET enabled = EXCLUDED.enabled, updated_at = now()`

func (r *ModulesRepo) Upsert(ctx context.Context, tenantID uuid.UUID, module string, enabled bool) error {
	_, err := r.pool.Exec(ctx, upsertTenantModule, tenantID, module, enabled)
	return err
}

// ReplaceForTenant siembra en una transaccion el estado completo de modulos del tenant.
func (r *ModulesRepo) ReplaceForTenant(ctx context.Context, tenantID uuid.UUID, states map[string]bool) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	for module, enabled := range states {
		if _, err := tx.Exec(ctx, upsertTenantModule, tenantID, module, enabled); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
