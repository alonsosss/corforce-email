// Package ports declara lo que el caso de uso necesita de fuera: la base de la empresa
// (campanas, lotes, estadisticas), la outbox y los tres servicios vecinos (contacts,
// transactional y templates).
package ports

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/alonsosss/corforce-email/services/campaigns/internal/domain"
	"github.com/google/uuid"
)

// Todos los repositorios resuelven el pool o la transaccion desde el contexto
// (db.ContextPool): cualquier operacion es valida dentro de Transact.

type Transactor interface {
	Transact(ctx context.Context, fn func(ctx context.Context) error) error
}

// ListFilter acota el listado de campanas. Los campos vacios no filtran.
type ListFilter struct {
	Status  domain.Status
	Search  string
	Page    int
	PerPage int
}

type CampaignRepository interface {
	// Insert crea la campana; domain.ErrNameTaken si el nombre ya esta en uso.
	Insert(ctx context.Context, c *domain.Campaign) error
	Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.Campaign, error)
	// GetForUpdate bloquea la fila hasta el fin de la transaccion (espera si otro la
	// tiene): toda transicion de estado pasa por aqui.
	GetForUpdate(ctx context.Context, tenantID, id uuid.UUID) (*domain.Campaign, error)
	// LockRunnable bloquea la campana si esta en envio y sin espera pendiente, SIN
	// esperar a quien la tenga (SKIP LOCKED). nil, nil si no esta disponible.
	LockRunnable(ctx context.Context, tenantID, id uuid.UUID, now time.Time) (*domain.Campaign, error)
	// Update guarda todo salvo los contadores, que solo se incrementan.
	Update(ctx context.Context, c *domain.Campaign) error
	Delete(ctx context.Context, tenantID, id uuid.UUID) error
	List(ctx context.Context, tenantID uuid.UUID, f ListFilter) ([]domain.Campaign, int64, error)
	// StartDue pasa a envio las programadas vencidas (SKIP LOCKED) y las devuelve.
	StartDue(ctx context.Context, tenantID uuid.UUID, now time.Time, limit int) ([]domain.Campaign, error)
	// ListRunnable devuelve las campanas en envio sin espera pendiente.
	ListRunnable(ctx context.Context, tenantID uuid.UUID, now time.Time, limit int) ([]uuid.UUID, error)
	// AddDeliveryTotals suma lo que devolvio la entrega de un lote.
	AddDeliveryTotals(ctx context.Context, tenantID, id uuid.UUID, targeted, accepted, suppressed int) error
}

type BatchRepository interface {
	// Pending devuelve el lote pendiente de la campana (hay a lo sumo uno), con su
	// pagina. nil, nil si no hay.
	Pending(ctx context.Context, tenantID, campaignID uuid.UUID) (*domain.Batch, error)
	// Last devuelve el lote de mayor numero, sin su pagina. nil, nil si no hay.
	Last(ctx context.Context, tenantID, campaignID uuid.UUID) (*domain.Batch, error)
	Insert(ctx context.Context, b *domain.Batch) error
	// Lease guarda la reserva (LeasedUntil, LeaseToken) de un lote pendiente.
	Lease(ctx context.Context, b *domain.Batch) error
	// SavePage fija la pagina, cursor_out y recipients del lote. false si ya no le
	// corresponde a quien lo intenta: el lote no esta pendiente, otro trabajador tomo la
	// reserva, ya tenia pagina o la campana dejo de estar en envio.
	SavePage(ctx context.Context, b *domain.Batch) (bool, error)
	// MarkDelivered cierra el lote pendiente; false si ya no estaba pendiente. Es la
	// unica puerta a los contadores de entrega: quien la cruza suma, nadie mas.
	MarkDelivered(ctx context.Context, tenantID, id uuid.UUID, accepted, suppressed int) (bool, error)
	// MarkFailed, RecordFailure y Release actuan solo si quien llama conserva la reserva
	// (LeaseToken) y la liberan.
	MarkFailed(ctx context.Context, b *domain.Batch, reason string) (bool, error)
	RecordFailure(ctx context.Context, b *domain.Batch, reason string) (attempts int, ok bool, err error)
	Release(ctx context.Context, b *domain.Batch, lastError string) (bool, error)
	// ResetAttempts pone a cero los intentos del lote pendiente (al reanudar).
	ResetAttempts(ctx context.Context, tenantID, campaignID uuid.UUID) error
	// DiscardPendingPages borra la pagina guardada de los lotes pendientes: al cancelar
	// o fallar la campana no queda copia de datos de contacto que ya no se va a usar.
	DiscardPendingPages(ctx context.Context, tenantID, campaignID uuid.UUID) error
	List(ctx context.Context, tenantID, campaignID uuid.UUID, page, perPage int) ([]domain.Batch, int64, error)
}

type StatsRepository interface {
	// MarkProcessed registra el evento; false si ya se habia contado.
	MarkProcessed(ctx context.Context, tenantID uuid.UUID, eventID string, at time.Time) (bool, error)
	// FirstEngagement registra la primera apertura o el primer clic del mensaje; false
	// si ya constaba. domain.ErrCampaignNotFound si la campana no existe.
	FirstEngagement(ctx context.Context, tenantID, campaignID, messageID uuid.UUID, kind domain.DeliveryKind, at time.Time) (bool, error)
	// IncrementCounter suma uno al contador del tipo; false si la campana no existe.
	IncrementCounter(ctx context.Context, tenantID, campaignID uuid.UUID, kind domain.DeliveryKind) (bool, error)
	PruneProcessed(ctx context.Context, tenantID uuid.UUID, before time.Time) (int64, error)
}

// EventPublisher encola los eventos propios. Se llama DENTRO de la transaccion: el
// evento existe si y solo si el cambio existe (outbox).
type EventPublisher interface {
	Publish(ctx context.Context, subject string, tenantID uuid.UUID, payload map[string]any) error
}

// ── Servicios vecinos ────────────────────────────────────────────────────────

// ErrUnavailable: el vecino no respondio o respondio algo que no se entiende. Es
// transitorio: reintentar puede arreglarlo.
var ErrUnavailable = errors.New("servicio dependiente no disponible")

// RateLimitedError: el vecino pide esperar (429) sin haber creado nada.
type RateLimitedError struct {
	RetryAfter time.Duration
}

func (e *RateLimitedError) Error() string {
	return fmt.Sprintf("limite de tasa alcanzado; reintentar en %s", e.RetryAfter)
}

// BlockedError: la empresa no puede enviar ahora (403 SENDING_RESTRICTED por reputacion
// o PLAN_LIMIT_REACHED). Reintentar no lo arregla; lo levanta una persona.
type BlockedError struct {
	Code    string
	Message string
}

func (e *BlockedError) Error() string { return e.Code + ": " + e.Message }

// RejectedError: la peticion no es valida y no lo sera al repetirla (dominio sin
// verificar, TEMPLATE_NOT_MARKETING, TEMPLATE_MISSING_UNSUBSCRIBE, audiencia
// inexistente). Code es el codigo de error del vecino y viaja al motivo del fallo.
type RejectedError struct {
	Code    string
	Message string
}

func (e *RejectedError) Error() string {
	if e.Code == "" {
		return e.Message
	}
	return e.Code + ": " + e.Message
}

type AudienceQuery struct {
	Audience domain.Audience
	// Cursor nil pide la primera pagina.
	Cursor *string
	Limit  int
}

type AudiencePage struct {
	Contacts []domain.Contact
	// NextCursor nil = no quedan paginas.
	NextCursor *string
}

// AudienceSource es contacts: POST /internal/contacts/audience. Solo devuelve contactos
// activos con consentimiento vigente.
type AudienceSource interface {
	Audience(ctx context.Context, tenantID uuid.UUID, q AudienceQuery) (*AudiencePage, error)
}

type BatchRequest struct {
	CampaignID uuid.UUID
	// CampaignName da el utm_campaign por defecto de los enlaces del lote.
	CampaignName    string
	IdempotencyKey  string
	FromEmail       string
	FromName        string
	ReplyTo         string
	TemplateID      uuid.UUID
	TemplateVersion int
	Recipients      []domain.Recipient
	Tags            map[string]string
}

type SuppressedRecipient struct {
	Email  string `json:"email"`
	Reason string `json:"reason"`
}

type BatchResult struct {
	Accepted   int                   `json:"accepted"`
	Suppressed []SuppressedRecipient `json:"suppressed"`
	MessageIDs []uuid.UUID           `json:"message_ids"`
}

// BatchSender es la via de marketing de transactional: POST
// /internal/transactional/batch. Con la misma clave de idempotencia devuelve el mismo
// resultado sin crear nada nuevo.
type BatchSender interface {
	SendBatch(ctx context.Context, tenantID uuid.UUID, r BatchRequest) (*BatchResult, error)
}

// TemplateCatalog averigua la version publicada de una plantilla. Errores:
// domain.ErrTemplateNotFound, domain.ErrNoPublishedVersion,
// domain.ErrTemplateNotMarketing, domain.ErrTemplateVersionRequired o ErrUnavailable.
type TemplateCatalog interface {
	PublishedVersion(ctx context.Context, tenantID, templateID uuid.UUID) (int, error)
}
