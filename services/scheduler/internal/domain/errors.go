package domain

import "errors"

var (
	ErrJobNotFound       = errors.New("job not found")
	ErrJobAlreadyExists  = errors.New("job already exists")
	ErrExecutionNotFound = errors.New("execution not found")
	ErrTaskNotFound      = errors.New("task not found")
	ErrJobLocked         = errors.New("job is locked")
	ErrInvalidCron       = errors.New("invalid cron expression")
	// ErrInvalidTimezone: la zona del trabajo no es un nombre IANA que la base de zonas cargue.
	ErrInvalidTimezone    = errors.New("invalid time zone")
	ErrMaxRetriesExceeded = errors.New("max retries exceeded")
	// ErrPlatformJob: los trabajos de plataforma (sin empresa) los define la plataforma;
	// por el API de una empresa se leen, pero no se cambian ni se lanzan.
	ErrPlatformJob = errors.New("platform jobs cannot be changed from a tenant")
	// ErrHandlerNotAllowed: el manejador no esta en el catalogo o no admite el tipo de
	// trabajo (de empresa o de plataforma).
	ErrHandlerNotAllowed = errors.New("handler not allowed")
	// ErrInvalidJob envuelve el motivo concreto por el que una definicion no es valida.
	ErrInvalidJob = errors.New("invalid job")
	// ErrInvalidReport: el cierre que informa un ejecutor no se puede guardar tal cual.
	ErrInvalidReport = errors.New("invalid execution report")
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
