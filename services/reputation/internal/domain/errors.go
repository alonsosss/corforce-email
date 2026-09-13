package domain

import "errors"

var (
	ErrInvalidClass   = errors.New("class debe ser transactional o marketing")
	ErrInvalidState   = errors.New("state debe ser ok, warning, restricted o suspended")
	ErrInvalidCount   = errors.New("count debe estar entre 1 y 10000")
	ErrInvalidLimit   = errors.New("limite de envio no valido")
	ErrInvalidPolicy  = errors.New("configuracion de reputacion no valida")
	ErrInvalidReason  = errors.New("reason es obligatorio y admite como maximo 500 caracteres")
	ErrInvalidEvent   = errors.New("evento de envio no valido")
	ErrTenantNotFound = errors.New("empresa no encontrada")
)
