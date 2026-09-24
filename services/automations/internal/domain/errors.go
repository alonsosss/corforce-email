package domain

import (
	"errors"
	"fmt"
)

var (
	ErrWorkflowNotFound = errors.New("flujo no encontrado")
	ErrRunNotFound      = errors.New("ejecución no encontrada")
	ErrNameTaken        = errors.New("ya existe un flujo con ese nombre")
	ErrNotEditable      = errors.New("el flujo solo se edita en borrador o en pausa")
	// ErrStepsLockedByRuns: las ejecuciones apuntan a su paso por posicion; cambiar los pasos con
	// ejecuciones abiertas las llevaria a otro paso (un correo que no les toca).
	ErrStepsLockedByRuns = errors.New("el flujo tiene ejecuciones en curso: sus pasos no cambian hasta que terminen o se cancelen; duplica el flujo para cambiarlos")
	ErrNotDeletable      = errors.New("solo se eliminan flujos en borrador o archivados")
	ErrInvalidTransition = errors.New("transición de estado no permitida")
	ErrNothingToUpdate   = errors.New("no hay cambios que aplicar")
	ErrConcurrentChange  = errors.New("el flujo cambió mientras se procesaba la petición; vuelva a intentarlo")

	// ErrInvalidInput agrupa los datos de entrada que no cumplen el contrato; el detalle
	// viaja en ValidationError.
	ErrInvalidInput = errors.New("datos no válidos")

	ErrTemplateNotFound   = errors.New("la plantilla no existe")
	ErrNoPublishedVersion = errors.New("la plantilla no tiene una versión publicada o está archivada")
	// ErrTemplateNotMarketing: un paso de envio solo admite plantillas de marketing, que
	// llevan baja y salen por el carril de marketing.
	ErrTemplateNotMarketing = errors.New("la plantilla no es de marketing")
	// ErrTemplateNotTransactional: el doble opt-in es un correo transaccional.
	ErrTemplateNotTransactional = errors.New("la plantilla del doble opt-in debe ser transaccional")
	// ErrTemplateMissingConfirmURL: la plantilla renderizada no muestra el enlace de
	// confirmacion, asi que la persona no podria confirmar.
	ErrTemplateMissingConfirmURL = errors.New("la plantilla no declara ni muestra la variable confirm_url")
	// ErrTemplateVariables: la plantilla exige variables que este uso no proporciona.
	ErrTemplateVariables = errors.New("la plantilla exige variables que no se proporcionan")
	// ErrTemplateVersionRequired: la version publicada no se pudo averiguar renderizando
	// sin variables; hay que indicar template_version en el paso.
	ErrTemplateVersionRequired = errors.New("no se pudo determinar la versión publicada de la plantilla; indique template_version")
	// ErrTemplateKindUnknown: templates no informo el tipo de la plantilla; sin el tipo no
	// se puede probar que sea la adecuada y se falla cerrado.
	ErrTemplateKindUnknown = errors.New("templates no informo el tipo de la plantilla")
)

// ValidationError describe que campo no cumple y por que. errors.Is(err,
// ErrInvalidInput) lo reconoce.
type ValidationError struct {
	msg string
}

func (e *ValidationError) Error() string { return e.msg }
func (e *ValidationError) Unwrap() error { return ErrInvalidInput }

func NewValidationError(format string, args ...any) error {
	return &ValidationError{msg: fmt.Sprintf(format, args...)}
}

// TransitionError describe una transicion que el ciclo de vida no admite.
func TransitionError(from, to Status) error {
	return fmt.Errorf("%w: de %s a %s", ErrInvalidTransition, from, to)
}
