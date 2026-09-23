package ports

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/services/templates/internal/deliverability"
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
	// CompileDraft declara como cadena opcional cada variable usada y no declarada; solo
	// para verificar un contenido que no se guarda.
	CompileDraft(c domain.Content) (CompiledTemplate, error)
}

// CompiledTemplate es una version compilada lista para renderizar con valores ya
// validados por domain.ResolveValues.
type CompiledTemplate interface {
	Render(values map[string]any) (domain.Rendered, error)
	// Variables son las declaradas, incluidas las que CompileDraft anadio.
	Variables() []domain.Variable
}

// EventPublisher emite los hechos del dominio. La implementacion encola en la outbox
// dentro de la transaccion en curso: el evento existe si y solo si el dato existe.
type EventPublisher interface {
	TemplatePublished(ctx context.Context, tenantID, templateID uuid.UUID, version int) error
}

// BrandKitRepository guarda el kit de marca de cada empresa.
type BrandKitRepository interface {
	// GetBrandKit devuelve nil, nil si la empresa no ha guardado ninguno.
	GetBrandKit(ctx context.Context, tenantID uuid.UUID) (*domain.BrandKit, error)
	// UpsertBrandKit guarda el kit y rellena UpdatedAt.
	UpsertBrandKit(ctx context.Context, k *domain.BrandKit) error
}

// AssetCursor es la posicion del ultimo elemento de una pagina de imagenes.
type AssetCursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

// AssetRepository guarda las imagenes de la empresa. Las retiradas (borrado logico) solo las
// devuelve GetAssetBySHA, para reactivarlas si se vuelven a subir.
type AssetRepository interface {
	// CreateAsset inserta la imagen; si ya hay una con el mismo sha256 en la empresa devuelve
	// domain.ErrAssetExists sin tocar nada.
	CreateAsset(ctx context.Context, a *domain.Asset) error
	GetAsset(ctx context.Context, tenantID, id uuid.UUID) (*domain.Asset, error)
	// GetAssetBySHA devuelve la imagen con ese contenido y si esta retirada;
	// domain.ErrAssetNotFound si no existe.
	GetAssetBySHA(ctx context.Context, tenantID uuid.UUID, sha256Hex string) (*domain.Asset, bool, error)
	// RestoreAsset vuelve a mostrar una imagen retirada con el nombre nuevo.
	RestoreAsset(ctx context.Context, tenantID, id uuid.UUID, name string) (*domain.Asset, error)
	// ListAssets devuelve hasta limit imagenes, de la mas reciente a la mas antigua, a partir
	// de after (nil = desde el principio).
	ListAssets(ctx context.Context, tenantID uuid.UUID, after *AssetCursor, limit int) ([]*domain.Asset, error)
	DeleteAsset(ctx context.Context, tenantID, id uuid.UUID) error
}

// AssetStore guarda el contenido de las imagenes en el almacen de objetos.
type AssetStore interface {
	Put(ctx context.Context, key string, data []byte, contentType string) error
	// PublicURL es la URL absoluta con la que los correos cargan la imagen.
	PublicURL(key string) string
}

// VirusScanner analiza un contenido antes de guardarlo. Devuelve domain.ErrAssetRejected si
// esta infectado y domain.ErrScannerUnavailable si no hubo veredicto limpio.
type VirusScanner interface {
	Scan(ctx context.Context, data []byte) error
}

// SpamSample es el correo renderizado que se puntua en el antispam.
type SpamSample struct {
	Marketing      bool
	Subject        string
	HTML           string
	Text           string
	UnsubscribeURL string
}

// SpamChecker puntua un correo con el Rspamd de la celda (mail-security). Un error significa
// que no hay puntuacion; la verificacion sigue sin ella.
type SpamChecker interface {
	Check(ctx context.Context, s SpamSample) (deliverability.Spam, error)
}
