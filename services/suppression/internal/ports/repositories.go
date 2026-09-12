package ports

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/services/suppression/internal/domain"
	"github.com/google/uuid"
)

// ListFilter acota el listado de exclusiones. Reason vacio = todas; Search se compara
// como subcadena del email.
type ListFilter struct {
	Reason  domain.Reason
	Search  string
	Page    int
	PerPage int
}

// EntryRepository persiste las exclusiones. El pool o la transaccion salen del contexto
// (db.ContextPool), asi que todas las operaciones son validas dentro de Transact.
type EntryRepository interface {
	GetByID(ctx context.Context, tenantID, id uuid.UUID) (*domain.Entry, error)
	// GetByEmailForUpdate bloquea la fila mientras dura la transaccion: dos ingestas de
	// la misma direccion no se pisan la causa. domain.ErrEntryNotFound si no existe.
	GetByEmailForUpdate(ctx context.Context, tenantID uuid.UUID, email string) (*domain.Entry, error)
	// FindByEmails devuelve las filas existentes para las direcciones dadas, vigentes o
	// no: la vigencia la decide el dominio (Entry.Active).
	FindByEmails(ctx context.Context, tenantID uuid.UUID, emails []string) ([]domain.Entry, error)
	List(ctx context.Context, tenantID uuid.UUID, f ListFilter) ([]domain.Entry, int64, error)
	// Insert crea la fila; domain.ErrEntryAlreadyExists si la direccion ya esta.
	Insert(ctx context.Context, e *domain.Entry) error
	// InsertMissing inserta en bloque las direcciones que aun no estan y devuelve las
	// que entraron. Las repetidas se omiten sin error.
	InsertMissing(ctx context.Context, tenantID uuid.UUID, emails []string, reason domain.Reason, source, detail string) ([]domain.Entry, error)
	Update(ctx context.Context, e *domain.Entry) error
	Delete(ctx context.Context, tenantID, id uuid.UUID) error
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
// evento existe si y solo si el cambio existe (outbox).
type EventPublisher interface {
	EntryAdded(ctx context.Context, e *domain.Entry) error
	EntryRemoved(ctx context.Context, e *domain.Entry) error
}
