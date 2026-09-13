package ports

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
	"github.com/google/uuid"
)

// Repository persiste en la base de la empresa. El pool (o la transaccion) se resuelve
// desde el contexto: TenantPoolMiddleware en el API, WithTenant en los consumidores.
type Repository interface {
	// Transact ejecuta fn dentro de una transaccion; el contexto que recibe fn enruta
	// todas las consultas (y la outbox) por ella.
	Transact(ctx context.Context, fn func(ctx context.Context) error) error

	InsertMessage(ctx context.Context, m *domain.Message) error
	GetMessage(ctx context.Context, tenantID, id uuid.UUID) (*domain.Message, error)
	// GetAttribution lee solo la clase, la campana y el contacto del mensaje: es lo que
	// necesitan los eventos de la ingesta y de la baja, sin cargar el cuerpo.
	GetAttribution(ctx context.Context, tenantID, id uuid.UUID) (*domain.MessageAttribution, error)
	GetMessages(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID) ([]domain.Message, error)
	ListMessages(ctx context.Context, tenantID uuid.UUID, f domain.MessageFilter, offset, limit int) ([]domain.Message, int64, error)
	// LockQueuedMessage carga el mensaje bloqueando su fila (FOR UPDATE) para que dos
	// workers no lo envien a la vez.
	LockQueuedMessage(ctx context.Context, tenantID, id uuid.UUID) (*domain.Message, error)
	MarkSent(ctx context.Context, tenantID, id uuid.UUID, sesMessageID string, sentAt time.Time) error
	MarkFailed(ctx context.Context, tenantID, id uuid.UUID, reason string) error
	// RecordAttempt suma un intento fallido transitorio y guarda el motivo.
	RecordAttempt(ctx context.Context, tenantID, id uuid.UUID, reason string) (attempts int, err error)
	// TransitionStatus cambia el estado solo si el actual esta en from; devuelve si cambio.
	TransitionStatus(ctx context.Context, tenantID, id uuid.UUID, to string, from []string) (bool, error)
	// ReleaseDue pasa a queued los mensajes programados vencidos (FOR UPDATE SKIP LOCKED)
	// y devuelve sus ids. Debe llamarse dentro de Transact.
	ReleaseDue(ctx context.Context, tenantID uuid.UUID, now time.Time, limit int) ([]uuid.UUID, error)
	CountByStatus(ctx context.Context, tenantID uuid.UUID, from, to time.Time) ([]domain.StatusCount, error)

	// InsertEvent devuelve false si ya existia un evento con el mismo sns_message_id.
	InsertEvent(ctx context.Context, e *domain.Event) (bool, error)
	ListEvents(ctx context.Context, tenantID, messageID uuid.UUID) ([]domain.Event, error)

	GetSubmission(ctx context.Context, tenantID uuid.UUID, key string) (*domain.Submission, error)
	// InsertSubmission devuelve false si la clave ya existia para la empresa.
	InsertSubmission(ctx context.Context, s *domain.Submission) (bool, error)

	UpsertSendingDomain(ctx context.Context, d *domain.SendingDomain) error
	DeleteSendingDomain(ctx context.Context, tenantID uuid.UUID, name string) error
	GetSendingDomain(ctx context.Context, tenantID uuid.UUID, name string) (*domain.SendingDomain, error)
	ListSendingDomains(ctx context.Context, tenantID uuid.UUID) ([]domain.SendingDomain, error)

	// InsertUnsubscribe devuelve false si la baja ya estaba registrada.
	InsertUnsubscribe(ctx context.Context, u *domain.Unsubscribe) (bool, error)
}

// EventPublisher encola eventos en la outbox de la base de la empresa. Debe invocarse
// dentro de Repository.Transact: el evento existe si y solo si el dato existe.
type EventPublisher interface {
	Publish(ctx context.Context, subject string, tenantID uuid.UUID, payload map[string]any) error
}

// Suppressed es una direccion que la lista de supresion rechaza. Es el mismo tipo que
// guarda la peticion para repetir su respuesta.
type Suppressed = domain.SuppressedRecipient

// SuppressionClient consulta y alimenta la lista de supresion de la empresa.
type SuppressionClient interface {
	Check(ctx context.Context, tenantID uuid.UUID, emails []string) ([]Suppressed, error)
	Add(ctx context.Context, tenantID uuid.UUID, entry SuppressionEntry) error
}

// SuppressionEntry es un alta en la lista de supresion.
type SuppressionEntry struct {
	Email     string `json:"email"`
	Reason    string `json:"reason"` // hard_bounce | complaint | unsubscribe
	Source    string `json:"source"`
	Detail    string `json:"detail,omitempty"`
	MessageID string `json:"message_id,omitempty"`
	// CampaignID atribuye la exclusion a la campana cuando el mensaje es de marketing.
	CampaignID string `json:"campaign_id,omitempty"`
}

// Authorization es la respuesta de reputation a "puede esta empresa enviar a N
// destinatarios de esta clase ahora". Una denegacion llega con Allowed false y su Reason.
type Authorization struct {
	Allowed bool
	Class   string
	State   string
	Reason  string
	// RetryAfterSeconds es nil cuando reputation no da espera (esperar no serviria).
	RetryAfterSeconds *int
}

// ReputationClient pide la autorizacion previa a un envio. Un error significa que
// reputation no dio respuesta (caido, 5xx, cuerpo ilegible), nunca una denegacion.
type ReputationClient interface {
	Authorize(ctx context.Context, tenantID uuid.UUID, class string, count int) (*Authorization, error)
}

// RenderRequest es lo que se le pide a templates por destinatario.
type RenderRequest struct {
	TemplateID uuid.UUID
	Version    *int
	Variables  map[string]any
	Reserved   ReservedVariables
}

// ReservedVariables son las variables que fija la plataforma y cambian por destinatario.
type ReservedVariables struct {
	UnsubscribeURL   string `json:"unsubscribe_url"`
	ViewInBrowserURL string `json:"view_in_browser_url"`
	RecipientEmail   string `json:"recipient_email"`
	TenantName       string `json:"tenant_name"`
}

// Rendered es la salida de templates. Kind es el tipo de la plantilla
// (domain.TemplateKind*); vacio si la version desplegada de templates no lo informa.
type Rendered struct {
	Subject string
	HTML    string
	Text    string
	Version int
	Kind    string
}

// TemplateRenderer renderiza una plantilla en templates.
type TemplateRenderer interface {
	Render(ctx context.Context, tenantID uuid.UUID, req RenderRequest) (*Rendered, error)
}

// Sender entrega el correo al proveedor. El error, cuando lo hay, es un
// *domain.SendError ya clasificado.
type Sender interface {
	Send(ctx context.Context, email domain.OutgoingEmail) (providerMessageID string, err error)
}
