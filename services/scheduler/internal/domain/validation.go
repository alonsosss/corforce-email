package domain

import (
	"strings"
	"time"
	"unicode/utf8"
)

// Topes de un trabajo y de una tarea puntual. GET /api/v1/scheduler/meta los publica tal
// cual. Los de texto son el ancho de su columna (varchar cuenta caracteres, no bytes):
// pasarse era un 500 de la base en vez de un 422 que dice que campo corregir.
const (
	// MaxNameLength es el ancho de job_definitions.name y de scheduled_tasks.name.
	MaxNameLength = 255
	// MaxCodeLength es el ancho de job_definitions.code.
	MaxCodeLength = 100
	// MaxHandlerNameLength es el ancho de job_definitions.handler y de scheduled_tasks.handler.
	MaxHandlerNameLength = 255
	// MaxDescriptionLength acota description, que es text: una descripcion, no un documento.
	MaxDescriptionLength = 2000
	// MaxPayloadBytes acota el payload en bytes: viaja entero en cada scheduler.job.started.
	MaxPayloadBytes = 64 << 10
	// MinIntervalMinutes es la resolucion del planificador (MinCronEvery).
	MinIntervalMinutes = int(MinCronEvery / time.Minute)
	// MaxIntervalMinutes es un ano. Un periodo mayor se expresa con un cron, que fija la
	// fecha; y la columna es integer, que sin tope desbordaba en la base.
	MaxIntervalMinutes = 365 * 24 * 60
	// MaxJobRetries acota max_retries. Con la espera creciente diez reintentos ya cubren
	// horas; mas solo prolonga un fallo que no se arregla solo.
	MaxJobRetries = 10
)

// Campos del contrato del API que nombra un FieldError.
const (
	FieldName            = "name"
	FieldCode            = "code"
	FieldDescription     = "description"
	FieldJobType         = "job_type"
	FieldCronExpression  = "cron_expression"
	FieldTimezone        = "timezone"
	FieldIntervalMinutes = "interval_minutes"
	FieldHandler         = "handler"
	FieldPayload         = "payload"
	FieldMaxRetries      = "max_retries"
	FieldTimeoutSeconds  = "timeout_seconds"
	FieldTriggerAt       = "trigger_at"
	FieldResult          = "result"
	// FieldReportError y FieldRetryable son los del cierre fallido que informa un ejecutor.
	FieldReportError = "error"
	FieldRetryable   = "retryable"
	// FieldIsActive es el filtro del listado de trabajos.
	FieldIsActive = "is_active"
	// FieldVersion es la version del trabajo que lleva una edicion.
	FieldVersion = "version"
)

// JobTypes son los tipos de trabajo admitidos, en el orden en que se ofrecen.
func JobTypes() []string { return []string{JobTypeCron, JobTypeInterval, JobTypeOneTime} }

// Validate comprueba lo que el ciclo de vida y las columnas dan por hecho de una definicion.
// Devuelve un FieldError con el primer campo que falla.
func (j *JobDefinition) Validate() error {
	if err := checkText(FieldName, ErrInvalidJob, j.Name, MaxNameLength, true); err != nil {
		return err
	}
	if err := checkText(FieldCode, ErrInvalidJob, j.Code, MaxCodeLength, true); err != nil {
		return err
	}
	if j.Description != nil {
		if err := checkText(FieldDescription, ErrInvalidJob, *j.Description, MaxDescriptionLength, false); err != nil {
			return err
		}
	}
	if err := checkText(FieldHandler, ErrInvalidJob, j.Handler, MaxHandlerNameLength, true); err != nil {
		return err
	}
	if _, err := LoadTimezone(j.Timezone); err != nil {
		return err
	}
	switch j.JobType {
	case JobTypeCron:
		if _, err := ParseCron(j.CronExpr(), j.Timezone); err != nil {
			return err
		}
	case JobTypeInterval:
		if j.IntervalMinutes == nil {
			return invalidField(FieldIntervalMinutes, RuleRequired, ErrInvalidJob, "interval_minutes is required for interval jobs")
		}
	case JobTypeOneTime:
	case "":
		return invalidField(FieldJobType, RuleRequired, ErrInvalidJob, "job_type is required (one of %s)", strings.Join(JobTypes(), ", "))
	default:
		return invalidField(FieldJobType, RuleNotAllowed, ErrInvalidJob, "job_type must be one of %s", strings.Join(JobTypes(), ", "))
	}
	// Los campos de otro tipo de trabajo no se usan, pero se guardan: tambien deben caber.
	if j.JobType != JobTypeCron && j.CronExpression != nil {
		if err := checkText(FieldCronExpression, ErrInvalidCron, *j.CronExpression, MaxCronExpressionLength, false); err != nil {
			return err
		}
	}
	if j.IntervalMinutes != nil && (*j.IntervalMinutes < MinIntervalMinutes || *j.IntervalMinutes > MaxIntervalMinutes) {
		return invalidField(FieldIntervalMinutes, RuleOutOfRange, ErrInvalidJob, "interval_minutes must be between %d and %d", MinIntervalMinutes, MaxIntervalMinutes)
	}
	if j.MaxRetries < 0 || j.MaxRetries > MaxJobRetries {
		return invalidField(FieldMaxRetries, RuleOutOfRange, ErrInvalidJob, "max_retries must be between 0 and %d", MaxJobRetries)
	}
	if j.TimeoutSeconds < 0 || j.TimeoutSeconds > MaxHandlerTimeoutSeconds {
		return invalidField(FieldTimeoutSeconds, RuleOutOfRange, ErrInvalidJob, "timeout_seconds must be between 0 and %d (0 takes the handler maximum)", MaxHandlerTimeoutSeconds)
	}
	return checkPayload(ErrInvalidJob, j.Payload)
}

// CheckTimeoutFor rechaza un plazo mayor que el maximo del manejador: se recortaria en
// silencio al despacharlo.
func (j *JobDefinition) CheckTimeoutFor(spec HandlerSpec) error {
	if j.TimeoutSeconds > spec.MaxTimeoutSeconds {
		return invalidField(FieldTimeoutSeconds, RuleOutOfRange, ErrInvalidJob, "timeout_seconds must be at most %d for handler %q (0 takes that maximum)", spec.MaxTimeoutSeconds, spec.Name)
	}
	return nil
}

// Validate comprueba una tarea puntual antes de guardarla.
func (t *ScheduledTask) Validate() error {
	if err := checkText(FieldName, ErrInvalidTask, t.Name, MaxNameLength, true); err != nil {
		return err
	}
	if t.Description != nil {
		if err := checkText(FieldDescription, ErrInvalidTask, *t.Description, MaxDescriptionLength, false); err != nil {
			return err
		}
	}
	if err := checkText(FieldHandler, ErrInvalidTask, t.Handler, MaxHandlerNameLength, true); err != nil {
		return err
	}
	if t.TriggerAt.IsZero() {
		return invalidField(FieldTriggerAt, RuleRequired, ErrInvalidTask, "trigger_at is required")
	}
	return checkPayload(ErrInvalidTask, t.Payload)
}

// checkText exige que value quepa en su columna y que Postgres lo acepte (UTF-8 valido y sin
// NUL); con required, ademas, que no este en blanco.
func checkText(field string, cause error, value string, max int, required bool) error {
	if required && strings.TrimSpace(value) == "" {
		return invalidField(field, RuleRequired, cause, "%s is required", field)
	}
	if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return invalidField(field, RuleInvalidFormat, cause, "%s must be valid UTF-8 text without the NUL character", field)
	}
	if utf8.RuneCountInString(value) > max {
		return invalidField(field, RuleTooLong, cause, "%s must be at most %d characters", field, max)
	}
	return nil
}

func checkPayload(cause error, payload *string) error {
	if payload == nil {
		return nil
	}
	if len(*payload) > MaxPayloadBytes {
		return invalidField(FieldPayload, RuleTooLong, cause, "payload must be at most %d bytes", MaxPayloadBytes)
	}
	if err := validJSONDocument([]byte(*payload)); err != nil {
		return invalidField(FieldPayload, RuleInvalidFormat, cause, "payload %s", err.Error())
	}
	return nil
}
