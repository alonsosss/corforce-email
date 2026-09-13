package domain

import "errors"

var (
	ErrInvalidResource      = errors.New("recurso no valido")
	ErrInvalidAmount        = errors.New("importe no valido")
	ErrInvalidPlan          = errors.New("plan no valido")
	ErrInvalidSubscription  = errors.New("suscripcion no valida")
	ErrInvalidQuantity      = errors.New("cantidad no valida")
	ErrInvalidEvent         = errors.New("evento no valido")
	ErrPlanNotFound         = errors.New("plan no encontrado")
	ErrUnknownPlanCode      = errors.New("no existe un plan con ese codigo")
	ErrPlanCodeTaken        = errors.New("ya existe un plan con ese codigo")
	ErrPlanInUse            = errors.New("el plan tiene suscripciones: sus condiciones no se editan, crea un plan nuevo")
	ErrPlanRetired          = errors.New("el plan esta retirado y no admite empresas nuevas")
	ErrSubscriptionNotFound = errors.New("la empresa no tiene suscripcion")
	ErrSubscriptionExists   = errors.New("la empresa ya tiene suscripcion")
)
