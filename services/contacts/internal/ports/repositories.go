package ports

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/alonsosss/corforce-email/services/contacts/internal/segment"
	"github.com/google/uuid"
)

// Todos los repositorios resuelven el pool o la transaccion desde el contexto
// (db.ContextPool): cualquier operacion es valida dentro de Transact.

// ContactFilter acota el listado de contactos. Los campos vacios no filtran.
type ContactFilter struct {
	Search  string
	Status  domain.Status
	Tag     string
	ListID  *uuid.UUID
	Page    int
	PerPage int
}

type ContactRepository interface {
	// Insert crea el contacto; domain.ErrContactExists si la direccion ya esta.
	Insert(ctx context.Context, c *domain.Contact) error
	GetByID(ctx context.Context, tenantID, id uuid.UUID) (*domain.Contact, error)
	// GetForUpdate bloquea la fila hasta el fin de la transaccion: todo cambio de
	// consentimiento pasa por aqui para que la evidencia se inserte en orden.
	GetForUpdate(ctx context.Context, tenantID, id uuid.UUID) (*domain.Contact, error)
	GetByEmailForUpdate(ctx context.Context, tenantID uuid.UUID, email string) (*domain.Contact, error)
	// Update guarda nombre, locale, zona, atributos, etiquetas y estado. El
	// consentimiento vigente no se escribe aqui: lo proyecta la base desde consents.
	Update(ctx context.Context, c *domain.Contact) error
	List(ctx context.Context, tenantID uuid.UUID, f ContactFilter) ([]domain.Contact, int64, error)
	// FindByEmailsForUpdate devuelve y bloquea los contactos existentes de esas
	// direcciones (importacion por lotes).
	FindByEmailsForUpdate(ctx context.Context, tenantID uuid.UUID, emails []string) ([]domain.Contact, error)
	// InsertMany inserta en bloque y devuelve los que entraron con su id; una direccion
	// que otro creo entre la lectura y la escritura se omite sin error.
	InsertMany(ctx context.Context, contacts []domain.Contact) ([]domain.Contact, error)
	UpdateMany(ctx context.Context, contacts []domain.Contact) error
	// Erase borra al contacto (membresias y tokens caen en cascada) y seudonimiza su
	// evidencia de consentimiento. Debe correr dentro de una transaccion.
	Erase(ctx context.Context, tenantID, id, pseudonym uuid.UUID, emailSHA256 string) error
	// StripAttribute quita la clave de todos los contactos (al retirar el atributo).
	StripAttribute(ctx context.Context, tenantID uuid.UUID, key string) error
}

// ConsentRepository solo inserta y lee: la evidencia no se modifica.
type ConsentRepository interface {
	Append(ctx context.Context, c *domain.Consent) error
	AppendMany(ctx context.Context, consents []domain.Consent) error
	ListByContact(ctx context.Context, tenantID, contactID uuid.UUID) ([]domain.Consent, error)
}

type TokenRepository interface {
	Create(ctx context.Context, t *domain.ConfirmationToken) error
	// DeleteUnused retira los enlaces pendientes del contacto: solo vale el ultimo.
	DeleteUnused(ctx context.Context, tenantID, contactID uuid.UUID) error
	// GetByHash y GetByHashForUpdate: domain.ErrInvalidConfirmation si no existe en esa
	// empresa. La primera solo lee (pagina de confirmacion); la segunda bloquea el token
	// para que dos confirmaciones simultaneas no lo usen dos veces.
	GetByHash(ctx context.Context, tenantID uuid.UUID, hash string) (*domain.ConfirmationToken, error)
	GetByHashForUpdate(ctx context.Context, tenantID uuid.UUID, hash string) (*domain.ConfirmationToken, error)
	MarkUsed(ctx context.Context, tenantID, id uuid.UUID, at time.Time) error
}

type ListRepository interface {
	Create(ctx context.Context, l *domain.List) error
	Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.List, error)
	Update(ctx context.Context, l *domain.List) error
	Delete(ctx context.Context, tenantID, id uuid.UUID) error
	List(ctx context.Context, tenantID uuid.UUID, page, perPage int) ([]domain.List, int64, error)
	// ExistingIDs devuelve cuales de los ids son listas de la empresa.
	ExistingIDs(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID) ([]uuid.UUID, error)
	// AddMembers anade los contactos de la empresa que existan; devuelve cuantos entraron.
	AddMembers(ctx context.Context, tenantID, listID uuid.UUID, contactIDs []uuid.UUID) (int, error)
	// RemoveMembers quita de la lista los contactos pedidos; devuelve cuantos salieron.
	RemoveMembers(ctx context.Context, tenantID, listID uuid.UUID, contactIDs []uuid.UUID) (int, error)
	ListsOf(ctx context.Context, tenantID, contactID uuid.UUID) ([]domain.List, error)
}

type AttributeRepository interface {
	List(ctx context.Context, tenantID uuid.UUID) ([]domain.AttributeDefinition, error)
	Get(ctx context.Context, tenantID uuid.UUID, key string) (*domain.AttributeDefinition, error)
	Create(ctx context.Context, d *domain.AttributeDefinition) error
	Update(ctx context.Context, d *domain.AttributeDefinition) error
	Delete(ctx context.Context, tenantID uuid.UUID, key string) error
}

type SegmentRepository interface {
	Create(ctx context.Context, s *domain.Segment) error
	Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.Segment, error)
	Update(ctx context.Context, s *domain.Segment) error
	Delete(ctx context.Context, tenantID, id uuid.UUID) error
	List(ctx context.Context, tenantID uuid.UUID, page, perPage int) ([]domain.Segment, int64, error)
	ListAll(ctx context.Context, tenantID uuid.UUID) ([]domain.Segment, error)
	GetMany(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID) ([]domain.Segment, error)
}

// AudienceSpec es una pagina de la audiencia de un envio: la union de listas y
// segmentos menos las exclusiones, solo contactos enviables, por keyset sobre id.
type AudienceSpec struct {
	ListIDs []uuid.UUID
	Include []segment.Definition
	Exclude []segment.Definition
	Schema  segment.Schema
	// After es el ultimo id de la pagina anterior; uuid.Nil = desde el principio.
	After uuid.UUID
	Limit int
}

// SegmentQuery evalua definiciones de segmento sobre los contactos. El adaptador las
// compila con internal/segment: el SQL nunca sale de ahi.
type SegmentQuery interface {
	Count(ctx context.Context, tenantID uuid.UUID, def segment.Definition, schema segment.Schema) (int64, error)
	Page(ctx context.Context, tenantID uuid.UUID, def segment.Definition, schema segment.Schema, limit, offset int) ([]domain.Contact, error)
	Audience(ctx context.Context, tenantID uuid.UUID, spec AudienceSpec) ([]domain.Contact, error)
}

type ImportRepository interface {
	Create(ctx context.Context, imp *domain.Import) error
	Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.Import, error)
	List(ctx context.Context, tenantID uuid.UUID, page, perPage int) ([]domain.Import, int64, error)
}

// Transactor abre la transaccion de negocio; db.ContextPool lo cumple.
type Transactor interface {
	Transact(ctx context.Context, fn func(ctx context.Context) error) error
}

// EventPublisher encola los eventos del dominio DENTRO de la transaccion (outbox): el
// evento existe si y solo si el cambio existe. Ningun payload lleva los atributos.
type EventPublisher interface {
	ContactCreated(ctx context.Context, c *domain.Contact) error
	ContactUpdated(ctx context.Context, c *domain.Contact, changed []string) error
	ContactDeleted(ctx context.Context, tenantID, contactID uuid.UUID) error
	ContactResubscribed(ctx context.Context, c *domain.Contact) error
	ConsentGranted(ctx context.Context, c *domain.Consent) error
	ConsentRevoked(ctx context.Context, c *domain.Consent) error
	ConsentRequested(ctx context.Context, c *domain.Contact, confirmURL string) error
	ImportCompleted(ctx context.Context, imp *domain.Import) error
}
