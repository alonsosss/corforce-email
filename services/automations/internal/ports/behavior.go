package ports

import (
	"context"
	"encoding/json"
	"time"

	"github.com/alonsosss/corforce-email/services/automations/internal/domain"
	"github.com/google/uuid"
)

// RunMessageRepository guarda el correo que envio cada paso send_email de una ejecucion y
// su apertura y su clic, para las ramas que los miran.
type RunMessageRepository interface {
	// Record registra el correo del paso; una repeticion (mismo run y paso) no hace nada.
	Record(ctx context.Context, m domain.RunMessage) error
	// Get devuelve nil, nil si el paso no envio nada (suprimido o no recorrido).
	Get(ctx context.Context, tenantID, runID uuid.UUID, stepID string) (*domain.RunMessage, error)
	// MarkEngagement anota la apertura o el clic del mensaje; idempotente (se conserva la
	// primera hora). false si el mensaje no es de ningun paso.
	MarkEngagement(ctx context.Context, tenantID, messageID uuid.UUID, openedAt, clickedAt *time.Time) (bool, error)
}

// DateScanRepository reparte el recorrido de aniversarios entre replicas.
type DateScanRepository interface {
	// Claim reserva el recorrido del flujo si el ultimo empezo antes de since; false si otra
	// replica lo hizo hace menos.
	Claim(ctx context.Context, tenantID, workflowID uuid.UUID, now, since time.Time) (bool, error)
}

// MatchQuery pregunta a contacts cuales de ContactIDs cumplen un segmento guardado o una
// definicion del DSL (exactamente una de las dos). Sin ids solo valida.
type MatchQuery struct {
	SegmentID  *uuid.UUID
	Definition json.RawMessage
	ContactIDs []uuid.UUID
}

// AnniversaryQuery es una tanda del recorrido de aniversarios de contacts.
type AnniversaryQuery struct {
	Attribute string
	Hour      int
	Timezone  string
	ListID    *uuid.UUID
	Cursor    string
	Limit     int
}

// AnniversaryMatch: Occurrence es la fecha local del aniversario (AAAA-MM-DD).
type AnniversaryMatch struct {
	ContactID  uuid.UUID
	Occurrence string
}

// AnniversaryPage: NextCursor vacio = recorrido terminado.
type AnniversaryPage struct {
	Matches    []AnniversaryMatch
	NextCursor string
}

// ContactRules es la parte de contacts que evalua reglas por contacto: las ramas por
// segmento o atributo y el recorrido de aniversarios. Un segmento o una lista que no
// existe es *RejectedError con status 404; una definicion o un atributo no valido, con 422.
type ContactRules interface {
	Match(ctx context.Context, tenantID uuid.UUID, q MatchQuery) ([]uuid.UUID, error)
	Anniversaries(ctx context.Context, tenantID uuid.UUID, q AnniversaryQuery) (*AnniversaryPage, error)
}
