package ports

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/services/suppression/internal/domain"
	"github.com/google/uuid"
)

// ListFilter acota el listado de direcciones excluidas. Reason vacio = todas; si no, la
// causa principal de la direccion. Search se compara como subcadena del email.
type ListFilter struct {
	Reason  domain.Reason
	Search  string
	Page    int
	PerPage int
}

// EntryRepository persiste las causas de exclusion, una fila por (empresa, direccion,
// causa). El pool o la transaccion salen del contexto (db.ContextPool), asi que todas las
// operaciones son validas dentro de Transact.
type EntryRepository interface {
	GetByID(ctx context.Context, tenantID, id uuid.UUID) (*domain.Entry, error)
	// LockAddress serializa hasta el fin de la transaccion los cambios sobre las causas
	// de una direccion: dos altas o retiradas simultaneas no se pisan y cada evento lleva
	// las causas que de verdad quedaron. Solo tiene efecto dentro de Transact.
	LockAddress(ctx context.Context, tenantID uuid.UUID, email string) error
	// GetCauseForUpdate devuelve y bloquea la fila de esa causa de la direccion, vigente o
	// caducada. domain.ErrEntryNotFound si la direccion no tiene esa causa.
	GetCauseForUpdate(ctx context.Context, tenantID uuid.UUID, email string, reason domain.Reason) (*domain.Entry, error)
	// FindByEmails devuelve todas las causas de las direcciones dadas, vigentes o no: la
	// vigencia y la causa principal las decide el dominio (domain.Aggregate).
	FindByEmails(ctx context.Context, tenantID uuid.UUID, emails []string) ([]domain.Entry, error)
	// ListAddresses pagina direcciones, no causas: devuelve las de la pagina, de la mas
	// reciente a la mas antigua por su causa principal, y el total que cumple el filtro.
	ListAddresses(ctx context.Context, tenantID uuid.UUID, f ListFilter, now time.Time) ([]string, int64, error)
	// Insert crea la fila; domain.ErrEntryAlreadyExists si la direccion ya tiene esa causa.
	Insert(ctx context.Context, e *domain.Entry) error
	// InsertMissing registra en bloque la causa en las direcciones que no la tienen y
	// reactiva, sin caducidad, la que la tenia caducada. Devuelve las filas que entraron o
	// se reactivaron; las que ya la tenian vigente se omiten sin error.
	InsertMissing(ctx context.Context, tenantID uuid.UUID, emails []string, reason domain.Reason, source, detail string, now time.Time) ([]domain.Entry, error)
	Update(ctx context.Context, e *domain.Entry) error
	// Reregister vuelve a registrar una causa existente (domain.Reason.RenewedOnRepeat):
	// guarda los datos de e y fija created_at con el reloj de la base en el momento de la
	// escritura, el mismo del DEFAULT de la columna. Deja en e created_at y updated_at.
	Reregister(ctx context.Context, e *domain.Entry) error
	Delete(ctx context.Context, tenantID, id uuid.UUID) error
	// CountByReason cuenta las direcciones con alguna causa vigente por su causa principal.
	CountByReason(ctx context.Context, tenantID uuid.UUID, now time.Time) ([]domain.ReasonCount, error)
}

// ImportRepository guarda el rastro de las cargas masivas.
type ImportRepository interface {
	Create(ctx context.Context, imp *domain.Import) error
	List(ctx context.Context, tenantID uuid.UUID, page, perPage int) ([]domain.Import, int64, error)
}

// Transactor abre la transaccion de negocio; db.ContextPool lo cumple.
type Transactor interface {
	Transact(ctx context.Context, fn func(ctx context.Context) error) error
}

// EventPublisher encola los eventos del dominio. Se llama DENTRO de la transaccion: el
// evento existe si y solo si el cambio existe (outbox). e es la causa que entro o se
// retiro; reasons, las causas vigentes que le quedan a la direccion tras el cambio.
type EventPublisher interface {
	EntryAdded(ctx context.Context, e *domain.Entry, reasons []domain.Reason) error
	EntryRemoved(ctx context.Context, e *domain.Entry, reasons []domain.Reason) error
}
