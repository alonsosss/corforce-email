package domain

import "errors"

var (
	ErrRoleNotFound       = errors.New("role not found")
	ErrRoleAlreadyExists  = errors.New("role already exists")
	ErrPermissionNotFound = errors.New("permission not found")
	ErrSystemRole         = errors.New("cannot modify system role")
	ErrPlatformPermission = errors.New("platform permissions cannot be assigned to tenant roles")
	// ErrSystemRoleAssignment: solo un rol del sistema asigna o retira un rol del sistema.
	ErrSystemRoleAssignment = errors.New("only a system role can assign or revoke a system role")
	// ErrPermissionNotHeld: nadie concede, directamente o a traves de un rol, un permiso
	// que el mismo no tiene.
	ErrPermissionNotHeld = errors.New("cannot grant a permission you do not hold")
	// ErrSystemRoleNameTaken: un rol propio de la empresa ocupa el nombre del rol del
	// sistema. Sembrar encima le daria todos los permisos de la empresa.
	ErrSystemRoleNameTaken = errors.New("a tenant role already uses the system role name")
	// ErrTenantActive: los roles de una empresa activa no se retiran en bloque.
	ErrTenantActive = errors.New("tenant is active")
	// ErrUserNotFound: la cuenta no existe en la empresa (borrada, o de otra empresa).
	ErrUserNotFound = errors.New("user not found")
	// ErrUserNotActive: la cuenta existe pero no esta activa (inactive, pending, o locked con
	// el bloqueo vigente).
	ErrUserNotActive = errors.New("user not active")
)
