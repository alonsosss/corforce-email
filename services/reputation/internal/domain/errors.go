package domain

import "errors"

var (
	ErrInvalidClass   = errors.New("class debe ser transactional o marketing")
	ErrInvalidState   = errors.New("state debe ser ok, warning, restricted o suspended")
	ErrInvalidCount   = errors.New("count debe estar entre 1 y 10000")
	ErrInvalidLimit   = errors.New("límite de envío no válido")
	ErrInvalidPolicy  = errors.New("configuración de reputación no válida")
	ErrInvalidReason  = errors.New("reason es obligatorio y admite como máximo 500 caracteres")
	ErrInvalidEvent   = errors.New("evento de envío no válido")
	ErrTenantNotFound = errors.New("empresa no encontrada")
)
