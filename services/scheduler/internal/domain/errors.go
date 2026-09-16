package domain

import (
	"errors"
	"fmt"
)

var (
	ErrJobNotFound       = errors.New("job not found")
	ErrJobAlreadyExists  = errors.New("job already exists")
	ErrExecutionNotFound = errors.New("execution not found")
	ErrTaskNotFound      = errors.New("task not found")
	// ErrTaskNotCancellable: la tarea ya se ejecuto; solo una programada se cancela.
	ErrTaskNotCancellable = errors.New("task already executed and can no longer be cancelled")
	ErrJobLocked          = errors.New("job is locked")
	ErrInvalidCron        = errors.New("invalid cron expression")
	// ErrInvalidTimezone: la zona del trabajo no es un nombre IANA que la base de zonas cargue.
	ErrInvalidTimezone    = errors.New("invalid time zone")
	ErrMaxRetriesExceeded = errors.New("max retries exceeded")
	// ErrPlatformJob: los trabajos de plataforma (sin empresa) los define la plataforma;
	// por el API de una empresa se leen, pero no se cambian ni se lanzan.
	ErrPlatformJob = errors.New("platform jobs cannot be changed from a tenant")
	// ErrOneTimeAlreadyRun: un trabajo one_time que el calendario ya despacho no se reactiva;
	// volver a lanzarlo es RunJob, que exige su propio permiso.
	ErrOneTimeAlreadyRun = errors.New("a one_time job that already ran cannot be re-enabled: run it manually or create a new job")
	// ErrJobVersionConflict: la edicion trae una version que ya no es la guardada; otra
	// edicion se aplico despues de su lectura y aplicarla la desharia.
	ErrJobVersionConflict = errors.New("the job changed after it was read: reload it and apply the changes again")
	// ErrHandlerNotAllowed: el manejador no esta en el catalogo o no admite el tipo de
	// trabajo (de empresa o de plataforma).
	ErrHandlerNotAllowed = errors.New("handler not allowed")
	// ErrInvalidJob envuelve el motivo concreto por el que una definicion no es valida.
	ErrInvalidJob = errors.New("invalid job")
	// ErrInvalidTask envuelve el motivo concreto por el que una tarea puntual no es valida.
	ErrInvalidTask = errors.New("invalid task")
	// ErrInvalidReport: el cierre que informa un ejecutor no se puede guardar tal cual.
	ErrInvalidReport = errors.New("invalid execution report")
	// ErrInvalidQuery: un parametro de la query de un listado no se puede leer.
	ErrInvalidQuery = errors.New("invalid query")
	// ErrExecutionConflict: se informa un resultado contrario al que ya consta (completar
	// una ejecucion fallida, o fallar una completada).
	ErrExecutionConflict = errors.New("execution already closed with a different outcome")
	// ErrExecutionClosed: se intenta cancelar una ejecucion que ya termino.
	ErrExecutionClosed = errors.New("execution already closed")
	// ErrExecutionNotRetryable: solo una ejecucion fallida admite un reintento manual.
	ErrExecutionNotRetryable = errors.New("only failed executions can be retried")
	// ErrAlreadyRetried: la ejecucion ya tiene un reintento, automatico o manual.
	ErrAlreadyRetried = errors.New("execution already has a retry")
)

// Reglas de un FieldError. Son contrato del API (error.details.rule): el cliente explica el
// error en su idioma por la regla y toma el limite de GET /scheduler/meta; el mensaje del
// servidor solo le sirve ante una regla que no conoce. Una regla no se renombra.
const (
	// RuleRequired: falta el valor o esta en blanco.
	RuleRequired = "required"
	// RuleTooLong: supera el ancho de su columna (caracteres) o, el payload, sus bytes.
	RuleTooLong = "too_long"
	// RuleOutOfRange: un numero o una duracion fuera de su rango.
	RuleOutOfRange = "out_of_range"
	// RuleInvalidFormat: no se puede leer (fecha, JSON, expresion, nombre de zona, texto con
	// NUL o que no es UTF-8).
	RuleInvalidFormat = "invalid_format"
	// RuleNotAllowed: bien escrito pero fuera de lo admitido (tipo de trabajo, descriptor,
	// zona que la base no carga, manejador fuera del catalogo o de otro alcance).
	RuleNotAllowed = "not_allowed"
	// RuleNeverMatches: una expresion cron valida que no ocurre en ninguna fecha.
	RuleNeverMatches = "never_matches"
	// RuleDuplicate: ya existe otro con ese valor (el codigo de un trabajo).
	RuleDuplicate = "duplicate"
)

// Rules son todas las reglas, para comprobar que ningun FieldError sale sin una conocida.
func Rules() []string {
	return []string{RuleRequired, RuleTooLong, RuleOutOfRange, RuleInvalidFormat, RuleNotAllowed, RuleNeverMatches, RuleDuplicate}
}

// FieldError es un dato de la peticion que no cumple su regla. Field es el nombre del campo
// en el contrato del API y Rule la regla que incumple, para que el cliente lo senale y lo
// explique sin interpretar el mensaje. Envuelve el error del dominio (ErrInvalidJob,
// ErrInvalidCron, ...), asi que errors.Is sigue reconociendolo.
type FieldError struct {
	Field  string
	Rule   string
	cause  error
	detail string
}

func (e *FieldError) Error() string { return e.cause.Error() + ": " + e.detail }

func (e *FieldError) Unwrap() error { return e.cause }

func invalidField(field, rule string, cause error, format string, args ...any) error {
	return &FieldError{Field: field, Rule: rule, cause: cause, detail: fmt.Sprintf(format, args...)}
}

// NewFieldError construye un FieldError desde fuera del dominio: un dato que se lee antes de
// llegar a el, como la fecha de una tarea o el cierre que informa un ejecutor.
func NewFieldError(field, rule string, cause error, detail string) error {
	return invalidField(field, rule, cause, "%s", detail)
}
