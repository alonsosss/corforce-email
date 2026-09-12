package domain

import "errors"

// Tiers del catalogo de modulos. Un modulo core no se puede deshabilitar.
const (
	ModuleTierCore     = "core"
	ModuleTierOptional = "optional"
)

// ModuleCatalogEntry es una fila del catalogo canonico de modulos de la plataforma.
type ModuleCatalogEntry struct {
	Module   string
	Tier     string
	Requires []string
	Label    string
	// PermissionModules son los modulos de permiso (columna module de
	// access_control.permissions) que este modulo de producto agrupa. Es la relacion
	// que leen access-control y el gateway para saber que permisos apaga un modulo
	// deshabilitado, y vive en la base para que ningun servicio la lleve en codigo.
	PermissionModules []string
}

// IsCore indica si el modulo no puede deshabilitarse.
func (m ModuleCatalogEntry) IsCore() bool { return m.Tier == ModuleTierCore }

// TenantModuleState es el estado de habilitacion de un modulo para un tenant.
type TenantModuleState struct {
	Module  string
	Enabled bool
}

var (
	ErrModuleNotFound = errors.New("modulo no encontrado en el catalogo")
	ErrModuleCore     = errors.New("un modulo esencial no puede deshabilitarse")
	ErrModuleRequired = errors.New("otro modulo habilitado depende de este")
)
