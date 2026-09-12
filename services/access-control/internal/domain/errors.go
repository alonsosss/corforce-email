package domain

import "errors"

var (
	ErrRoleNotFound       = errors.New("role not found")
	ErrRoleAlreadyExists  = errors.New("role already exists")
	ErrPermissionNotFound = errors.New("permission not found")
	ErrSystemRole         = errors.New("cannot modify system role")
)
