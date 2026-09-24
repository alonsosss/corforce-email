package domain

import "errors"

var (
	ErrInvalidResource      = errors.New("recurso no válido")
	ErrInvalidAmount        = errors.New("importe no válido")
	ErrInvalidPlan          = errors.New("plan no válido")
	ErrInvalidSubscription  = errors.New("suscripción no válida")
	ErrInvalidQuantity      = errors.New("cantidad no válida")
	ErrInvalidEvent         = errors.New("evento no válido")
	ErrPlanNotFound         = errors.New("plan no encontrado")
	ErrUnknownPlanCode      = errors.New("no existe un plan con ese código")
	ErrPlanCodeTaken        = errors.New("ya existe un plan con ese código")
	ErrPlanInUse            = errors.New("el plan tiene suscripciones: sus condiciones no se editan, crea un plan nuevo")
	ErrPlanRetired          = errors.New("el plan está retirado y no admite empresas nuevas")
	ErrSubscriptionNotFound = errors.New("la empresa no tiene suscripción")
	ErrSubscriptionExists   = errors.New("la empresa ya tiene suscripción")
)
