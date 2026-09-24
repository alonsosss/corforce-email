package domain

import (
	"time"

	"github.com/google/uuid"
)

// AssistantSettings es el interruptor del asistente del webmail de una empresa
// (mail.assistant_settings, docs/adr/0014). Sin fila la empresa lo tiene apagado.
type AssistantSettings struct {
	TenantID uuid.UUID
	Enabled  bool
	// UpdatedBy es el usuario de la plataforma que hizo el ultimo cambio; nil si nunca se cambio.
	UpdatedBy *uuid.UUID
	// EnabledAt es la ultima activacion, que es cuando se acepto el aviso de tratamiento de datos.
	EnabledAt *time.Time
	UpdatedAt time.Time
}

// NewAssistantSettings es el estado de una empresa que nunca lo cambio: apagado.
func NewAssistantSettings(tenantID uuid.UUID) *AssistantSettings {
	return &AssistantSettings{TenantID: tenantID}
}

// Apply cambia el interruptor. Solo una activacion que antes estaba apagada mueve EnabledAt: volver a
// guardar el mismo estado no reescribe el instante en que se acepto el aviso.
func (s *AssistantSettings) Apply(enabled bool, by uuid.UUID, now time.Time) {
	if enabled && (!s.Enabled || s.EnabledAt == nil) {
		// Microsegundos, la precision de timestamptz: lo devuelto coincide con lo que se relee.
		at := now.UTC().Truncate(time.Microsecond)
		s.EnabledAt = &at
	}
	s.Enabled = enabled
	if by != uuid.Nil {
		u := by
		s.UpdatedBy = &u
	}
}
