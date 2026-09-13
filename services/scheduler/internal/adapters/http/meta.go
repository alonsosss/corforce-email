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

type metaResponse struct {
	Timezone metaTimezone `json:"timezone"`
	Cron     metaCron     `json:"cron"`
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
	}
}
