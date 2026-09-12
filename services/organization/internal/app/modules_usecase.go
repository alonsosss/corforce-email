package app

import (
	"context"
	"errors"
	"sort"

	"github.com/alonsosss/corforce-email/services/organization/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// ModuleInfo describe un modulo del catalogo junto con su estado efectivo para un
// tenant. Lo consume la consola de plataforma.
type ModuleInfo struct {
	Module            string   `json:"module"`
	Tier              string   `json:"tier"`
	Requires          []string `json:"requires"`
	Label             string   `json:"label"`
	PermissionModules []string `json:"permission_modules"`
	Enabled           bool     `json:"enabled"`
}

// MigrationSweepResult resume un barrido de migraciones sobre todos los tenants.
type MigrationSweepResult struct {
	Migrated int `json:"migrated"`
	Failed   int `json:"failed"`
	// Locked: tenants que otra instancia estaba migrando en ese momento. No es un fallo.
	Locked int `json:"locked"`
}

// TenantMigrationInfo es el estado de migraciones canonicas de un tenant para el
// plano de control.
type TenantMigrationInfo struct {
	TenantID  uuid.UUID `json:"tenant_id"`
	Slug      string    `json:"slug"`
	DBName    string    `json:"db_name"`
	Status    string    `json:"status"` // ok | pending | error
	Applied   int       `json:"applied"`
	Pending   []string  `json:"pending"`
	Baselined bool      `json:"baselined"`
	Error     string    `json:"error,omitempty"`
}

// MigrateAllTenants aplica las migraciones canonicas pendientes a la base de cada
// tenant activo, de forma idempotente (reutiliza el tracking en public.schema_migrations
// del provisioner). Un fallo en un tenant no aborta los demas: sin esto las migraciones
// canonicas solo correrian al crear la base del tenant.
func (uc *OrganizationUseCase) MigrateAllTenants(ctx context.Context) (MigrationSweepResult, error) {
	var res MigrationSweepResult
	err := uc.activeTenants(ctx, func(t *domain.Tenant) {
		target, err := uc.targetFor(ctx, t)
		if err != nil {
			uc.logger.Error("fallo al localizar la base del tenant", zap.String("slug", t.Slug), zap.Error(err))
			res.Failed++
			return
		}
		switch e := uc.provisioner.RunMigrations(ctx, target); {
		case e == nil:
			res.Migrated++
		case errors.Is(e, domain.ErrMigrationsLocked):
			uc.logger.Info("migraciones del tenant en curso en otra instancia",
				zap.String("slug", t.Slug), zap.String("db", t.DBName))
			res.Locked++
		default:
			uc.logger.Error("fallo al migrar el tenant",
				zap.String("slug", t.Slug), zap.String("db", t.DBName), zap.Error(e))
			res.Failed++
		}
	})
	return res, err
}

// MigrateTenant aplica las migraciones pendientes a un solo tenant. Sirve para reintentar
// el que quedo en error sin barrer los demas.
func (uc *OrganizationUseCase) MigrateTenant(ctx context.Context, tenantID uuid.UUID) error {
	t, err := uc.tenants.GetByID(ctx, tenantID)
	if err != nil {
		return err
	}
	target, err := uc.targetFor(ctx, t)
	if err != nil {
		return err
	}
	return uc.provisioner.RunMigrations(ctx, target)
}

// TenantMigrationStatuses reporta, por tenant activo, cuantas migraciones canonicas tiene
// aplicadas y cuales le faltan: una base desalineada se detecta desde el plano de
// control en vez de descubrirse cuando un modulo falla.
func (uc *OrganizationUseCase) TenantMigrationStatuses(ctx context.Context) ([]TenantMigrationInfo, error) {
	out := make([]TenantMigrationInfo, 0, 8)
	err := uc.activeTenants(ctx, func(t *domain.Tenant) {
		info := TenantMigrationInfo{TenantID: t.ID, Slug: t.Slug, DBName: t.DBName}
		var st domain.TenantMigrationStatus
		target, err := uc.targetFor(ctx, t)
		if err == nil {
			st, err = uc.provisioner.MigrationStatus(ctx, target)
		}
		switch {
		case err != nil:
			info.Status = "error"
			info.Error = err.Error()
		case len(st.Pending) > 0:
			info.Status = "pending"
		default:
			info.Status = "ok"
		}
		info.Applied = st.Applied
		info.Pending = st.Pending
		info.Baselined = st.Baselined
		out = append(out, info)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GetTenantModules devuelve el catalogo con el estado efectivo del tenant. Sin filas
// propias, todos los modulos se reportan habilitados.
func (uc *OrganizationUseCase) GetTenantModules(ctx context.Context, tenantID uuid.UUID) ([]ModuleInfo, error) {
	catalog, err := uc.modules.ListCatalog(ctx)
	if err != nil {
		return nil, err
	}
	states, err := uc.modules.ListForTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	out := effectiveModules(catalog, states)
	sort.Slice(out, func(i, j int) bool {
		ci, cj := out[i].Tier == domain.ModuleTierCore, out[j].Tier == domain.ModuleTierCore
		if ci != cj {
			return ci
		}
		return out[i].Module < out[j].Module
	})
	return out, nil
}

// effectiveModules cruza el catalogo con las filas del tenant. Con filas, un modulo
// ausente cuenta como deshabilitado (el catalogo explicito es completo); los core
// siempre estan habilitados.
func effectiveModules(catalog []domain.ModuleCatalogEntry, states []domain.TenantModuleState) []ModuleInfo {
	stateMap := make(map[string]bool, len(states))
	for _, s := range states {
		stateMap[s.Module] = s.Enabled
	}
	hasCatalog := len(states) > 0

	out := make([]ModuleInfo, 0, len(catalog))
	for _, e := range catalog {
		enabled := true
		if hasCatalog && !e.IsCore() {
			enabled = stateMap[e.Module]
		}
		out = append(out, ModuleInfo{
			Module:            e.Module,
			Tier:              e.Tier,
			Requires:          e.Requires,
			Label:             e.Label,
			PermissionModules: e.PermissionModules,
			Enabled:           enabled,
		})
	}
	return out
}

// SetTenantModule habilita o deshabilita un modulo para un tenant, validando integridad:
//   - los modulos core no se pueden deshabilitar;
//   - habilitar arrastra las dependencias (requires) transitivas;
//   - deshabilitar se rechaza si otro modulo habilitado lo requiere (transitivamente).
//
// Al terminar publica el estado efectivo completo.
func (uc *OrganizationUseCase) SetTenantModule(ctx context.Context, tenantID uuid.UUID, module string, enabled bool) error {
	if _, err := uc.tenants.GetByID(ctx, tenantID); err != nil {
		return domain.ErrTenantNotFound
	}
	catalog, err := uc.modules.ListCatalog(ctx)
	if err != nil {
		return err
	}
	byModule := make(map[string]domain.ModuleCatalogEntry, len(catalog))
	reqMap := make(map[string][]string, len(catalog))
	for _, e := range catalog {
		byModule[e.Module] = e
		reqMap[e.Module] = e.Requires
	}
	target, ok := byModule[module]
	if !ok {
		return domain.ErrModuleNotFound
	}
	if target.IsCore() && !enabled {
		return domain.ErrModuleCore
	}

	// Si el tenant aun no tiene catalogo explicito, se siembra una linea base con todo
	// habilitado antes de aplicar el cambio: asi tocar un modulo no apaga los demas.
	hasAny, err := uc.modules.HasAny(ctx, tenantID)
	if err != nil {
		return err
	}
	if !hasAny {
		base := make(map[string]bool, len(catalog))
		for _, e := range catalog {
			base[e.Module] = true
		}
		if err := uc.modules.ReplaceForTenant(ctx, tenantID, base); err != nil {
			return err
		}
	}

	if enabled {
		for m := range closureRequires(module, reqMap) {
			if err := uc.modules.Upsert(ctx, tenantID, m, true); err != nil {
				return err
			}
		}
	} else {
		states, err := uc.modules.ListForTenant(ctx, tenantID)
		if err != nil {
			return err
		}
		enabledNow := make(map[string]bool, len(states))
		for _, s := range states {
			enabledNow[s.Module] = s.Enabled
		}
		for _, e := range catalog {
			if e.Module != module && enabledNow[e.Module] && requiresTransitively(e.Module, module, reqMap) {
				return domain.ErrModuleRequired
			}
		}
		if err := uc.modules.Upsert(ctx, tenantID, module, false); err != nil {
			return err
		}
	}

	uc.announceModules(ctx, tenantID, catalog)
	return nil
}

// announceModules publica el estado efectivo de modulos del tenant tras un cambio.
func (uc *OrganizationUseCase) announceModules(ctx context.Context, tenantID uuid.UUID, catalog []domain.ModuleCatalogEntry) {
	if uc.publisher == nil {
		return
	}
	states, err := uc.modules.ListForTenant(ctx, tenantID)
	if err != nil {
		uc.logger.Warn("no se pudo leer el estado de modulos para anunciarlo",
			zap.String("tenant_id", tenantID.String()), zap.Error(err))
		return
	}
	enabled, disabled := []string{}, []string{}
	for _, m := range effectiveModules(catalog, states) {
		if m.Enabled {
			enabled = append(enabled, m.Module)
		} else {
			disabled = append(disabled, m.Module)
		}
	}
	sort.Strings(enabled)
	sort.Strings(disabled)
	uc.publish("tenant.modules_changed", tenantID, func() error {
		return uc.publisher.TenantModulesChanged(ctx, tenantID, enabled, disabled)
	})
}

// closureRequires devuelve el modulo y todas sus dependencias transitivas.
func closureRequires(module string, reqMap map[string][]string) map[string]bool {
	set := map[string]bool{}
	var visit func(m string)
	visit = func(m string) {
		if set[m] {
			return
		}
		set[m] = true
		for _, dep := range reqMap[m] {
			visit(dep)
		}
	}
	visit(module)
	return set
}

// requiresTransitively indica si `module` depende de `target` directa o transitivamente.
func requiresTransitively(module, target string, reqMap map[string][]string) bool {
	seen := map[string]bool{}
	var visit func(m string) bool
	visit = func(m string) bool {
		for _, dep := range reqMap[m] {
			if dep == target {
				return true
			}
			if !seen[dep] {
				seen[dep] = true
				if visit(dep) {
					return true
				}
			}
		}
		return false
	}
	return visit(module)
}
