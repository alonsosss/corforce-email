package domain

import "github.com/google/uuid"

// SystemRoleSeed es el resultado de sembrar el rol del sistema de una empresa: el rol, si
// nacio en esta llamada y cuantos permisos del catalogo recibio en ella (0 al repetirla).
type SystemRoleSeed struct {
	Role    *Role
	Created bool
	Granted int64
}

// SystemRoleReseed resume la reaplicacion del catalogo a los roles del sistema de todas las
// empresas: cuantos roles recorrio y cuantos permisos les faltaban.
type SystemRoleReseed struct {
	Roles   int64
	Granted int64
}

// TenantRolesRemoval es lo que se retiro al borrar los roles de una empresa: cuantos roles y
// a que usuarios se les quito alguna asignacion (su politica cacheada deja de valer).
type TenantRolesRemoval struct {
	Roles int64
	Users []uuid.UUID
}
