package domain

import (
	"sort"
	"time"

	"github.com/google/uuid"
)

type Role struct {
	ID          uuid.UUID
	TenantID    uuid.UUID
	Name        string
	Description string
	IsSystem    bool
	Status      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type Permission struct {
	ID          uuid.UUID
	Module      string
	Resource    string
	Action      string
	Description string
}

type AccessPolicy struct {
	UserID      uuid.UUID
	TenantID    uuid.UUID
	Roles       []Role
	Permissions []Permission
}

// HasPermission resuelve la triple (module, resource, action) admitiendo el comodin "*"
// en resource y action: un permiso "campaigns/*/*" cubre todo el modulo y "campaigns/
// templates/*" cubre todas las acciones sobre ese recurso.
func (ap *AccessPolicy) HasPermission(module, resource, action string) bool {
	for _, p := range ap.Permissions {
		if p.Module != module {
			continue
		}
		if p.Resource == resource && p.Action == action {
			return true
		}
		if p.Resource == "*" && p.Action == "*" {
			return true
		}
		if p.Resource == resource && p.Action == "*" {
			return true
		}
	}
	return false
}

func (ap *AccessPolicy) HasRole(roleName string) bool {
	for _, r := range ap.Roles {
		if r.Name == roleName {
			return true
		}
	}
	return false
}

func (ap *AccessPolicy) RoleNames() []string {
	names := make([]string, 0, len(ap.Roles))
	for _, r := range ap.Roles {
		names = append(names, r.Name)
	}
	return names
}

// ModuleAvailability es el resultado de cruzar el catalogo de modulos contratables con el
// estado de habilitacion del tenant, ya traducido al vocabulario de los permisos (la
// columna module de access_control.permissions).
type ModuleAvailability struct {
	// Restricted indica que el tenant tiene estado explicito. Sin el, no se filtra nada.
	Restricted bool
	// Gated recoge cada modulo de permiso que algun modulo del catalogo reclama y si el
	// tenant lo tiene habilitado. Un modulo que ningun modulo del catalogo reclama no se
	// gatea: el plano de control (identity, access, audit...) no se contrata, siempre esta.
	Gated map[string]bool
}

func (m ModuleAvailability) Allows(module string) bool {
	if !m.Restricted {
		return true
	}
	enabled, gated := m.Gated[module]
	return !gated || enabled
}

// Disabled devuelve, ordenados, los modulos de permiso que el tenant tiene apagados.
func (m ModuleAvailability) Disabled() []string {
	out := make([]string, 0)
	if !m.Restricted {
		return out
	}
	for module, enabled := range m.Gated {
		if !enabled {
			out = append(out, module)
		}
	}
	sort.Strings(out)
	return out
}

// AccessDenial es un intento de escritura bloqueado (o que se habria bloqueado en modo
// auditoria) por el control RBAC del gateway.
type AccessDenial struct {
	ID        uuid.UUID
	TenantID  uuid.UUID
	UserID    uuid.UUID
	Module    string
	Action    string
	Method    string
	Path      string
	Enforced  bool
	CreatedAt time.Time
}

// DenialSummaryRow agrega denegaciones por una dimension (modulo o usuario).
type DenialSummaryRow struct {
	Key   string
	Count int64
}
