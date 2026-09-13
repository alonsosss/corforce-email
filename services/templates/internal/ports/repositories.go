package ports

import (
	"context"

	"github.com/alonsosss/corforce-email/services/templates/internal/domain"
	"github.com/google/uuid"
)

// ListFilter acota el listado de plantillas. Los campos vacios no filtran.
type ListFilter struct {
	Kind   string
	Status string
	Search string
	Offset int
	Limit  int
}

// Repository persiste plantillas y versiones en la base de la empresa. El pool se
// resuelve desde el contexto (TenantPoolMiddleware o TenantHeaderPoolMiddleware) y las
// escrituras que deben ir juntas se envuelven con Transactor.
type Repository interface {
	CreateTemplate(ctx context.Context, t *domain.Template) error
	GetTemplate(ctx context.Context, tenantID, id uuid.UUID) (*domain.Template, error)
	// GetTemplateForUpdate bloquea la fila hasta el fin de la transaccion: serializa la
	// numeracion de versiones y la publicacion entre peticiones concurrentes.
	GetTemplateForUpdate(ctx context.Context, tenantID, id uuid.UUID) (*domain.Template, error)
	ListTemplates(ctx context.Context, tenantID uuid.UUID, f ListFilter) ([]*domain.Template, int64, error)
	UpdateTemplate(ctx context.Context, t *domain.Template) error
	DeleteTemplate(ctx context.Context, tenantID, id uuid.UUID) error

	CreateVersion(ctx context.Context, v *domain.Version) error
	GetVersion(ctx context.Context, tenantID, templateID uuid.UUID, version int) (*domain.Version, error)
	GetPublishedVersion(ctx context.Context, tenantID, templateID uuid.UUID) (*domain.Version, error)
	// ListVersions devuelve las versiones sin contenido, de la mas reciente a la primera.
	ListVersions(ctx context.Context, tenantID, templateID uuid.UUID) ([]domain.VersionSummary, error)
	// MaxVersion devuelve el numero mas alto de la plantilla, 0 si no tiene versiones.
	MaxVersion(ctx context.Context, tenantID, templateID uuid.UUID) (int, error)
	// SupersedePublished pasa a supersedida la version publicada de la plantilla, si la hay.
	SupersedePublished(ctx context.Context, tenantID, templateID uuid.UUID) error
	MarkPublished(ctx context.Context, tenantID, versionID uuid.UUID) error
}

// Transactor ejecuta fn dentro de una transaccion; el contexto que recibe fn enruta todas
// las operaciones del repositorio y de la outbox por ella.
type Transactor interface {
	Transact(ctx context.Context, fn func(ctx context.Context) error) error
}

// Renderer compila y ejecuta el contenido de una version (internal/render).
type Renderer interface {
	Compile(c domain.Content) (CompiledTemplate, error)
}

// CompiledTemplate es una version compilada lista para renderizar con valores ya
// validados por domain.ResolveValues.
type CompiledTemplate interface {
	Render(values map[string]any) (domain.Rendered, error)
}

// EventPublisher emite los hechos del dominio. La implementacion encola en la outbox
// dentro de la transaccion en curso: el evento existe si y solo si el dato existe.
type EventPublisher interface {
	TemplatePublished(ctx context.Context, tenantID, templateID uuid.UUID, version int) error
}
