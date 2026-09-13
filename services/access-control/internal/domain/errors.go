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
)
