package ports

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/alonsosss/corforce-email/services/contacts/internal/segment"
	"github.com/google/uuid"
)

// EngagementRepository es la proyeccion de interaccion (contacts.engagement).
type EngagementRepository interface {
	// Touch aplica el toque a la fila (contacto, campana) con LEAST/GREATEST: idempotente
	// y sin importar el orden. false si el contacto no existe en la empresa (se borro).
	Touch(ctx context.Context, t domain.EngagementTouch) (bool, error)
	// Prune borra hasta limit filas sin actividad desde before; devuelve cuantas.
	Prune(ctx context.Context, tenantID uuid.UUID, before time.Time, limit int) (int64, error)
}

// AnniversaryQuery es una tanda del recorrido de aniversarios: contactos que tienen la
// clave con un valor cuyo "MM-DD" esta en MonthDays, despues de After en orden de id.
type AnniversaryQuery struct {
	Attribute string
	MonthDays []string
	ListID    *uuid.UUID
	After     uuid.UUID
	Limit     int
}

// AnniversaryCandidate es un contacto prefiltrado con lo justo para decidir: el valor del
// atributo y su zona.
type AnniversaryCandidate struct {
	ID       uuid.UUID
	Value    string
	Timezone *string
}

// ContactMatcher evalua por contacto lo que pregunta automations: cuales de unos ids cumplen
// una definicion y los candidatos a un aniversario.
type ContactMatcher interface {
	MatchAmong(ctx context.Context, tenantID uuid.UUID, def segment.Definition, schema segment.Schema, ids []uuid.UUID) ([]uuid.UUID, error)
	AnniversaryCandidates(ctx context.Context, tenantID uuid.UUID, q AnniversaryQuery) ([]AnniversaryCandidate, error)
}
