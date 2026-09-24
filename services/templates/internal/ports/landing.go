package ports

import (
	"context"

	"github.com/alonsosss/corforce-email/services/templates/internal/domain"
	"github.com/google/uuid"
)

// PageFilter acota el listado de paginas de aterrizaje. Los campos vacios no filtran.
type PageFilter struct {
	Status string
	Search string
	Offset int
	Limit  int
}

// PageRepository persiste las paginas de aterrizaje y sus versiones en la base de la empresa.
type PageRepository interface {
	// CreatePage: domain.ErrPageNameTaken o domain.ErrPageSlugTaken si chocan.
	CreatePage(ctx context.Context, p *domain.LandingPage) error
	GetPage(ctx context.Context, tenantID, id uuid.UUID) (*domain.LandingPage, error)
	// GetPageForUpdate bloquea la fila: serializa la numeracion y la publicacion.
	GetPageForUpdate(ctx context.Context, tenantID, id uuid.UUID) (*domain.LandingPage, error)
	// GetPageBySlug devuelve la pagina de la empresa con esa direccion.
	GetPageBySlug(ctx context.Context, tenantID uuid.UUID, slug string) (*domain.LandingPage, error)
	ListPages(ctx context.Context, tenantID uuid.UUID, f PageFilter) ([]*domain.LandingPage, int64, error)
	UpdatePage(ctx context.Context, p *domain.LandingPage) error
	DeletePage(ctx context.Context, tenantID, id uuid.UUID) error

	CreatePageVersion(ctx context.Context, v *domain.LandingVersion) error
	GetPageVersion(ctx context.Context, tenantID, pageID uuid.UUID, version int) (*domain.LandingVersion, error)
	ListPageVersions(ctx context.Context, tenantID, pageID uuid.UUID) ([]domain.VersionSummary, error)
	MaxPageVersion(ctx context.Context, tenantID, pageID uuid.UUID) (int, error)
	// SupersedePublishedPage pasa a supersedida la version publicada de la pagina, si la hay.
	SupersedePublishedPage(ctx context.Context, tenantID, pageID uuid.UUID) error
	MarkPageVersionPublished(ctx context.Context, tenantID, versionID uuid.UUID) error
}

// TenantDirectory da el slug de una empresa, con el que se forma la direccion publica de sus
// paginas.
type TenantDirectory interface {
	TenantSlug(ctx context.Context, tenantID string) (string, error)
}
