package ports

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

// AssistantProvider completa una peticion del asistente con el proveedor externo (docs/adr/0014). No
// guarda nada. Un proveedor saturado o que limita tras sus reintentos es domain.ErrAssistantBusy; uno que
// declina, domain.ErrAssistantRefused; cualquier otro fallo, domain.ErrAssistantFailed.
type AssistantProvider interface {
	Complete(ctx context.Context, p domain.AssistantPrompt) (domain.AssistantCompletion, error)
}

// AssistantSettings dice si la empresa del buzon activo el asistente (mail-directory, dueno del ajuste).
// La empresa la resuelve el directorio a partir del buzon. Un fallo es domain.ErrUnavailable.
type AssistantSettings interface {
	AssistantEnabled(ctx context.Context, username string) (bool, error)
}

// AssistantQuota cuenta las peticiones del dia por buzon y por empresa y aplica los topes.
type AssistantQuota interface {
	// Consume anota una peticion si ni el buzon ni la empresa llegaron a su tope del dia de at (UTC). Si
	// alguno llego, no anota nada y devuelve *domain.AssistantQuotaError.
	Consume(ctx context.Context, tenantID, username string, at time.Time, limits domain.AssistantLimits) (domain.AssistantUsage, error)
	// Usage devuelve lo consumido en el dia de at sin anotar nada.
	Usage(ctx context.Context, tenantID, username string, at time.Time) (domain.AssistantUsage, error)
}

// AssistantAudit deja constancia de cada uso en la auditoria de la empresa, sin contenido.
type AssistantAudit interface {
	AssistantUsed(ctx context.Context, rec domain.AssistantUsageRecord) error
}

// AssistantMetrics mide usos y tokens. Las etiquetas son de conjuntos cerrados (accion, resultado,
// modelo configurado).
type AssistantMetrics interface {
	AssistantRequest(action domain.AssistantAction, outcome string)
	AssistantTokens(model string, input, output int)
	AssistantLatency(action domain.AssistantAction, d time.Duration)
	// AssistantAuditFailed cuenta los usos que no dejaron apunte de auditoria.
	AssistantAuditFailed()
}

// AssistantSource lee del buzon de la sesion lo que el asistente puede enviar de un mensaje, sin
// marcarlo como leido.
type AssistantSource interface {
	AssistantMessages(ctx context.Context, sess domain.Session, refs []domain.MessageRef) ([]domain.AssistantSourceMessage, error)
}
