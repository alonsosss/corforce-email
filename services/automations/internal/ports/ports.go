// Package ports declara lo que el caso de uso necesita de fuera: la base de la empresa
// (ajustes y entregas del doble opt-in, flujos, ejecuciones, eventos procesados), la
// outbox y los tres servicios vecinos (transactional, contacts y templates).
package ports

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/alonsosss/corforce-email/services/automations/internal/domain"
	"github.com/google/uuid"
)

// Todos los repositorios resuelven el pool o la transaccion desde el contexto
// (db.ContextPool): cualquier operacion es valida dentro de Transact.

type Transactor interface {
	Transact(ctx context.Context, fn func(ctx context.Context) error) error
}

type SettingsRepository interface {
	// GetDOISettings devuelve nil, nil si la empresa aun no tiene ajustes.
	GetDOISettings(ctx context.Context, tenantID uuid.UUID) (*domain.DOISettings, error)
	UpsertDOISettings(ctx context.Context, s *domain.DOISettings) error
}

type DeliveryRepository interface {
	// LockContact serializa hasta el fin de la transaccion los intentos de un mismo
	// contacto: sin ello, dos eventos simultaneos contarian cero envios y los dos saldrian.
	LockContact(ctx context.Context, tenantID, contactID uuid.UUID) error
	// GetByEvent devuelve nil, nil si el evento no tiene intento.
	GetByEvent(ctx context.Context, tenantID uuid.UUID, eventID string) (*domain.DOIDelivery, error)
	// CountRecent cuenta los envios hechos o en vuelo (sent y pending) del contacto desde
	// cada instante, sin contar el del evento dado.
	CountRecent(ctx context.Context, tenantID, contactID uuid.UUID, dayStart, monthStart time.Time, excludeEventID string) (lastDay, last30Days int, err error)
	Insert(ctx context.Context, d *domain.DOIDelivery) error
	// MarkSent, MarkFailed y RecordAttempt solo actuan sobre un intento pending.
	MarkSent(ctx context.Context, tenantID, id uuid.UUID, messageID *uuid.UUID, at time.Time) (bool, error)
	MarkFailed(ctx context.Context, tenantID, id uuid.UUID, reason string) (bool, error)
	RecordAttempt(ctx context.Context, tenantID, id uuid.UUID, reason string) (attempts int, err error)
	List(ctx context.Context, tenantID uuid.UUID, status domain.DOIStatus, page, perPage int) ([]domain.DOIDelivery, int64, error)
}

// WorkflowFilter acota el listado de flujos. Los campos vacios no filtran.
type WorkflowFilter struct {
	Status  domain.Status
	Search  string
	Page    int
	PerPage int
}

type WorkflowRepository interface {
	// Insert crea el flujo; domain.ErrNameTaken si el nombre ya esta en uso.
	Insert(ctx context.Context, w *domain.Workflow) error
	Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.Workflow, error)
	// GetForUpdate bloquea la fila hasta el fin de la transaccion: toda transicion de
	// estado y toda edicion pasan por aqui.
	GetForUpdate(ctx context.Context, tenantID, id uuid.UUID) (*domain.Workflow, error)
	Update(ctx context.Context, w *domain.Workflow) error
	Delete(ctx context.Context, tenantID, id uuid.UUID) error
	List(ctx context.Context, tenantID uuid.UUID, f WorkflowFilter) ([]domain.Workflow, int64, error)
	ListActiveByTrigger(ctx context.Context, tenantID uuid.UUID, trigger domain.TriggerType) ([]domain.Workflow, error)
}

// RunFilter acota el listado de ejecuciones de un flujo.
type RunFilter struct {
	WorkflowID uuid.UUID
	Status     domain.RunStatus
	Page       int
	PerPage    int
}

type RunRepository interface {
	// Enroll inserta la ejecucion si no hay otra del mismo evento y, si el flujo no admite
	// reentrada, ninguna otra del contacto. false si no entro.
	Enroll(ctx context.Context, r *domain.Run, reEntry bool) (bool, error)
	// ClaimDue reserva hasta limit ejecuciones debidas de flujos activos (FOR UPDATE SKIP
	// LOCKED) y confirma la reserva antes de devolverlas.
	ClaimDue(ctx context.Context, tenantID uuid.UUID, now, leaseUntil time.Time, limit int) ([]domain.Run, error)
	Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.Run, error)
	List(ctx context.Context, tenantID uuid.UUID, f RunFilter) ([]domain.Run, int64, error)
	// Advance, Reschedule, Finish y Release solo actuan si quien llama conserva la reserva
	// (LeaseToken) y la liberan; false si otro trabajador la tomo o la ejecucion cambio.
	Advance(ctx context.Context, r *domain.Run, nextIndex int, nextRunAt time.Time) (bool, error)
	Reschedule(ctx context.Context, r *domain.Run, nextRunAt time.Time, attempts int, code, reason string) (bool, error)
	Finish(ctx context.Context, r *domain.Run, status domain.RunStatus, code, reason string, at time.Time) (bool, error)
	Release(ctx context.Context, r *domain.Run) (bool, error)
	CancelByWorkflow(ctx context.Context, tenantID, workflowID uuid.UUID, at time.Time) (int64, error)
	// CountRecentFailures cuenta las ejecuciones del flujo fallidas con alguno de los
	// codigos desde since.
	CountRecentFailures(ctx context.Context, tenantID, workflowID uuid.UUID, codes []string, since time.Time) (int, error)
}

type ProcessedRepository interface {
	IsProcessed(ctx context.Context, tenantID uuid.UUID, eventID string) (bool, error)
	// MarkProcessed registra el evento; false si ya estaba.
	MarkProcessed(ctx context.Context, tenantID uuid.UUID, eventID string, at time.Time) (bool, error)
	PruneProcessed(ctx context.Context, tenantID uuid.UUID, before time.Time) (int64, error)
}

// EventPublisher encola los eventos propios DENTRO de la transaccion (outbox): el evento
// existe si y solo si el cambio existe.
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
	return fmt.Sprintf("%s: reintentar en %s", domain.CodeRateLimited, e.RetryAfter)
}

// BlockedError: la empresa no puede enviar ahora (403 SENDING_RESTRICTED o
// PLAN_LIMIT_REACHED). Reintentar no lo arregla.
type BlockedError struct {
	Code    string
	Message string
}

func (e *BlockedError) Error() string { return e.Code + ": " + e.Message }

// RejectedError: la peticion no es valida y no lo sera al repetirla (400, 404, 409, 422).
// Code es el codigo de error del vecino y viaja al motivo.
type RejectedError struct {
	Status  int
	Code    string
	Message string
}

func (e *RejectedError) Error() string {
	if e.Code == "" {
		return e.Message
	}
	return e.Code + ": " + e.Message
}

// NotFound: el recurso nombrado (una lista, una plantilla) no existe.
func (e *RejectedError) NotFound() bool { return e.Status == http.StatusNotFound }

// Suppressed es una direccion que transactional retiro por la lista de supresion.
type Suppressed struct {
	Email  string `json:"email"`
	Reason string `json:"reason"`
}

// DOIMessage es el correo de confirmacion: transaccional, de la empresa, con proposito
// double_opt_in (transactional no lo bloquea por una baja voluntaria).
type DOIMessage struct {
	IdempotencyKey string
	FromEmail      string
	FromName       string
	ReplyTo        string
	ToEmail        string
	ToName         string
	TemplateID     uuid.UUID
	Variables      map[string]any
	Tags           map[string]string
}

// DOIResult: el mensaje creado (o el ya creado con la misma clave). Status suppressed
// indica que la lista de supresion lo bloqueo.
type DOIResult struct {
	MessageID  *uuid.UUID
	Status     string
	Suppressed []Suppressed
}

// MessageStatusSuppressed es el estado con que transactional registra un mensaje que la
// lista de supresion bloqueo.
const MessageStatusSuppressed = "suppressed"

// MarketingMessage es un envio de un paso send_email: un lote de un destinatario por la
// via de marketing, con campaign_id = id del flujo.
type MarketingMessage struct {
	WorkflowID      uuid.UUID
	IdempotencyKey  string
	FromEmail       string
	FromName        string
	ReplyTo         string
	TemplateID      uuid.UUID
	TemplateVersion int
	Contact         domain.Contact
	Tags            map[string]string
}

type BatchResult struct {
	Accepted   int
	Suppressed []Suppressed
	MessageIDs []uuid.UUID
}

// Sender es transactional: POST /internal/transactional/messages (doble opt-in) y POST
// /internal/transactional/batch (marketing). Con la misma clave devuelve lo ya creado.
type Sender interface {
	SendDOI(ctx context.Context, tenantID uuid.UUID, m DOIMessage) (*DOIResult, error)
	SendMarketing(ctx context.Context, tenantID uuid.UUID, m MarketingMessage) (*BatchResult, error)
}

// Contacts es contacts por su API interna. Sendable solo devuelve los enviables (activos
// con consentimiento de marketing vigente); ListMembers, los ids que son miembros de la
// lista. Una lista inexistente es *RejectedError con status 404.
type Contacts interface {
	Sendable(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID) ([]domain.Contact, error)
	ListMembers(ctx context.Context, tenantID, listID uuid.UUID, ids []uuid.UUID) ([]uuid.UUID, error)
	AddToList(ctx context.Context, tenantID, listID uuid.UUID, ids []uuid.UUID) error
	RemoveFromList(ctx context.Context, tenantID, listID uuid.UUID, ids []uuid.UUID) error
}

// RenderRequest pide un render interno a templates. Version nil = la publicada.
type RenderRequest struct {
	TemplateID uuid.UUID
	Version    *int
	Variables  map[string]json.RawMessage
}

type Rendered struct {
	Subject string
	HTML    string
	Text    string
	Version int
	Kind    string
}

// Templates renderiza en templates. Errores: domain.ErrTemplateNotFound,
// domain.ErrNoPublishedVersion, domain.ErrTemplateVariables o ErrUnavailable.
type Templates interface {
	Render(ctx context.Context, tenantID uuid.UUID, req RenderRequest) (*Rendered, error)
}
