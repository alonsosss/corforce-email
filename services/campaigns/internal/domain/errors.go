package domain

import (
	"errors"
	"fmt"
)

var (
	ErrCampaignNotFound  = errors.New("campana no encontrada")
	ErrNameTaken         = errors.New("ya existe una campana con ese nombre")
	ErrInvalidTransition = errors.New("transicion de estado no permitida")
	ErrNotEditable       = errors.New("la campana solo se puede editar en borrador o en pausa")
	ErrLockedWhilePaused = errors.New("en pausa no se cambian la audiencia ni la plantilla")
	ErrNotDeletable      = errors.New("solo se eliminan campanas en borrador o canceladas")
	ErrNothingToUpdate   = errors.New("no hay cambios que aplicar")
	ErrScheduleInPast    = errors.New("la fecha de programacion debe quedar en el futuro")
	ErrScheduleTooFar    = errors.New("la fecha de programacion supera el horizonte permitido")
	ErrConcurrentChange  = errors.New("la campana cambio mientras se procesaba la peticion; vuelva a intentarlo")
	// ErrABWithTimezone: la ventana de decision de una prueba A/B exige que toda la muestra
	// salga a la vez, y el envio por zona horaria la repartiria en tramos.
	ErrABWithTimezone   = errors.New("la prueba A/B no se combina con el envio por zona horaria")
	ErrABAlreadyDecided = errors.New("la prueba A/B ya tiene ganadora")

	// ErrInvalidCampaign agrupa los datos de entrada que no cumplen el contrato; los
	// detalles viajan en ValidationError.
	ErrInvalidCampaign = errors.New("datos de campana no validos")

	ErrTemplateNotFound   = errors.New("la plantilla no existe")
	ErrNoPublishedVersion = errors.New("la plantilla no tiene una version publicada")
	// ErrTemplateNotMarketing: una campana solo sale con plantillas de marketing; las
	// transaccionales no llevan baja ni salen por el carril de marketing.
	ErrTemplateNotMarketing = errors.New("la plantilla no es de marketing")
	// ErrTemplateVersionRequired: templates no pudo renderizar la version publicada sin
	// variables (declara alguna requerida sin valor por defecto), asi que su numero no se
	// puede averiguar desde aqui y quien programa debe indicarlo.
	ErrTemplateVersionRequired = errors.New("no se pudo determinar la version publicada de la plantilla; indique template_version")
)

// ValidationError describe que campo no cumple y por que. errors.Is(err,
// ErrInvalidCampaign) lo reconoce.
type ValidationError struct {
	msg string
}

func (e *ValidationError) Error() string { return e.msg }
func (e *ValidationError) Unwrap() error { return ErrInvalidCampaign }

func NewValidationError(format string, args ...any) error {
	return &ValidationError{msg: fmt.Sprintf(format, args...)}
}

// TransitionError describe una transicion que el ciclo de vida no admite.
func TransitionError(from, to Status) error {
	return fmt.Errorf("%w: de %s a %s", ErrInvalidTransition, from, to)
}
