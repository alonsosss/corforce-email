package http

import (
	"net/http"
	"time"

	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/services/scheduler/internal/domain"
)

// metaTimezone es la regla de la zona de un trabajo. No hay lista cerrada: vale cualquier
// nombre IANA con esa forma que cargue la base de zonas del servicio.
type metaTimezone struct {
	Default   string `json:"default"`
	Format    string `json:"format"`
	Pattern   string `json:"pattern"`
	MaxLength int    `json:"max_length"`
}

type metaCron struct {
	Descriptors     []string `json:"descriptors"`
	MinEverySeconds int      `json:"min_every_seconds"`
	MaxLength       int      `json:"max_length"`
}

// metaLimits son los topes de un trabajo. max_name_length y max_handler_length valen tambien
// para una tarea puntual; timeout_seconds 0 toma el maximo del manejador, que ademas no
// puede superarse (max_timeout_seconds de GET /handlers). Los topes de la expresion cron y
// de la zona van en cron y timezone.
type metaLimits struct {
	MaxNameLength        int `json:"max_name_length"`
	MaxCodeLength        int `json:"max_code_length"`
	MaxDescriptionLength int `json:"max_description_length"`
	MaxHandlerLength     int `json:"max_handler_length"`
	MaxPayloadBytes      int `json:"max_payload_bytes"`
	MinIntervalMinutes   int `json:"min_interval_minutes"`
	MaxIntervalMinutes   int `json:"max_interval_minutes"`
	MaxRetries           int `json:"max_retries"`
	MaxTimeoutSeconds    int `json:"max_timeout_seconds"`
}

type metaPagination struct {
	DefaultPerPage int `json:"default_per_page"`
	MaxPerPage     int `json:"max_per_page"`
}

// metaResponse son las reglas de los trabajos (jobs/read). La ventana de las tareas
// pendientes no va aqui: la lleva la meta de GET /tasks, que se lee con tasks/read.
type metaResponse struct {
	Timezone   metaTimezone   `json:"timezone"`
	Cron       metaCron       `json:"cron"`
	JobTypes   []string       `json:"job_types"`
	Limits     metaLimits     `json:"limits"`
	Pagination metaPagination `json:"pagination"`
}

// timezoneFormat nombra la base de la que salen las zonas validas.
const timezoneFormat = "iana"

// Meta publica las reglas de un trabajo para que la UI no las copie, tomadas del dominio.
func (h *Handler) Meta(w http.ResponseWriter, r *http.Request) {
	response.JSON(w, http.StatusOK, buildMeta())
}

func buildMeta() metaResponse {
	return metaResponse{
		Timezone: metaTimezone{
			Default:   domain.DefaultTimezone,
			Format:    timezoneFormat,
			Pattern:   domain.TimezonePattern(),
			MaxLength: domain.MaxTimezoneLength,
		},
		Cron: metaCron{
			Descriptors:     domain.CronDescriptors(),
			MinEverySeconds: int(domain.MinCronEvery / time.Second),
			MaxLength:       domain.MaxCronExpressionLength,
		},
		JobTypes: domain.JobTypes(),
		Limits: metaLimits{
			MaxNameLength:        domain.MaxNameLength,
			MaxCodeLength:        domain.MaxCodeLength,
			MaxDescriptionLength: domain.MaxDescriptionLength,
			MaxHandlerLength:     domain.MaxHandlerNameLength,
			MaxPayloadBytes:      domain.MaxPayloadBytes,
			MinIntervalMinutes:   domain.MinIntervalMinutes,
			MaxIntervalMinutes:   domain.MaxIntervalMinutes,
			MaxRetries:           domain.MaxJobRetries,
			MaxTimeoutSeconds:    domain.MaxHandlerTimeoutSeconds,
		},
		Pagination: metaPagination{DefaultPerPage: defaultPerPage, MaxPerPage: maxPerPage},
	}
}
