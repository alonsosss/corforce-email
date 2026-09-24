package app

import (
	"context"
	"errors"
	"sync"

	"github.com/alonsosss/corforce-email/services/templates/internal/domain"
	"github.com/alonsosss/corforce-email/services/templates/internal/landing"
	"github.com/alonsosss/corforce-email/services/templates/internal/ports"
	"github.com/google/uuid"
)

// PublicPagePath es donde sirve el gateway las paginas publicadas: <base>/p/<empresa>/<slug>.
const PublicPagePath = "/p/"

// ErrPagesUnavailable: el servicio arranco sin lo necesario para las paginas de aterrizaje.
var ErrPagesUnavailable = errors.New("las páginas de aterrizaje no están disponibles")

// maxRenderedPages acota la cache de documentos servidos; una version publicada no cambia, asi
// que su documento se arma una vez.
const maxRenderedPages = 512

// renderedKey lleva noindex porque es de la pagina, no de la version, y cambia el documento.
type renderedKey struct {
	version uuid.UUID
	noIndex bool
}

type renderedPages struct {
	mu    sync.Mutex
	items map[renderedKey][]byte
}

func (c *renderedPages) get(id renderedKey) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.items[id]
	return b, ok
}

func (c *renderedPages) put(id renderedKey, b []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.items == nil || len(c.items) >= maxRenderedPages {
		c.items = make(map[renderedKey][]byte)
	}
	c.items[id] = b
}

func (uc *UseCase) pagesReady() error {
	if uc.pages == nil || uc.tenants == nil || uc.publicBaseURL == "" {
		return ErrPagesUnavailable
	}
	return nil
}

// PagePublicPrefix es la direccion bajo la que cuelgan las paginas de la empresa.
func (uc *UseCase) PagePublicPrefix(ctx context.Context, tenantID uuid.UUID) (string, error) {
	if err := uc.pagesReady(); err != nil {
		return "", err
	}
	slug, err := uc.tenants.TenantSlug(ctx, tenantID.String())
	if err != nil {
		return "", err
	}
	return uc.publicBaseURL + PublicPagePath + slug + "/", nil
}

// PageContentInput es el contenido de una version tal como llega del editor.
type PageContentInput struct {
	Title       string
	Description string
	HTML        string
	CSS         string
	Editor      *domain.PageEditorDocument
}

// preparePageContent valida y sanea: lo que se guarda ya es lo que se sirve.
func preparePageContent(in PageContentInput) (domain.PageContent, error) {
	title, desc, err := domain.NormalizePageMeta(in.Title, in.Description)
	if err != nil {
		return domain.PageContent{}, err
	}
	html, err := landing.SanitizeHTML(in.HTML)
	if err != nil {
		return domain.PageContent{}, err
	}
	if err := landing.CheckCSS(in.CSS); err != nil {
		return domain.PageContent{}, err
	}
	editor, err := domain.NormalizePageEditor(in.Editor)
	if err != nil {
		return domain.PageContent{}, err
	}
	return domain.PageContent{Title: title, Description: desc, HTML: html, CSS: in.CSS, Editor: editor}, nil
}

// CreatePageInput es el alta de una pagina, con su primera version opcional.
type CreatePageInput struct {
	Name    string
	Slug    string
	NoIndex bool
	Content *PageContentInput
}

func (uc *UseCase) CreatePage(ctx context.Context, tenantID, userID uuid.UUID, in CreatePageInput) (*domain.LandingDetail, error) {
	if err := uc.pagesReady(); err != nil {
		return nil, err
	}
	if userID == uuid.Nil {
		return nil, domain.ErrMissingCreator
	}
	name, err := domain.NormalizePageName(in.Name)
	if err != nil {
		return nil, err
	}
	slug, err := domain.NormalizePageSlug(in.Slug)
	if err != nil {
		return nil, err
	}
	var content *domain.PageContent
	if in.Content != nil {
		c, err := preparePageContent(*in.Content)
		if err != nil {
			return nil, err
		}
		content = &c
	}
	p := &domain.LandingPage{
		ID: uuid.New(), TenantID: tenantID, Name: name, Slug: slug, Status: domain.PageStatusActive,
		NoIndex: in.NoIndex, CreatedBy: userID,
	}
	err = uc.tx.Transact(ctx, func(ctx context.Context) error {
		if err := uc.pages.CreatePage(ctx, p); err != nil {
			return err
		}
		if content == nil {
			return nil
		}
		return uc.pages.CreatePageVersion(ctx, newPageVersion(p, 1, *content, userID))
	})
	if err != nil {
		return nil, err
	}
	return uc.GetPage(ctx, tenantID, p.ID)
}

func newPageVersion(p *domain.LandingPage, n int, c domain.PageContent, userID uuid.UUID) *domain.LandingVersion {
	return &domain.LandingVersion{
		ID: uuid.New(), TenantID: p.TenantID, PageID: p.ID, Version: n, Content: c,
		Status: domain.VersionStatusDraft, CreatedBy: userID,
	}
}

func (uc *UseCase) GetPage(ctx context.Context, tenantID, id uuid.UUID) (*domain.LandingDetail, error) {
	if err := uc.pagesReady(); err != nil {
		return nil, err
	}
	p, err := uc.pages.GetPage(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	detail := &domain.LandingDetail{Page: p}
	if p.CurrentVersion > 0 {
		if detail.Current, err = uc.pages.GetPageVersion(ctx, tenantID, id, p.CurrentVersion); err != nil {
			return nil, err
		}
	}
	if detail.Versions, err = uc.pages.ListPageVersions(ctx, tenantID, id); err != nil {
		return nil, err
	}
	return detail, nil
}

func (uc *UseCase) ListPages(ctx context.Context, tenantID uuid.UUID, f ports.PageFilter) ([]*domain.LandingPage, int64, error) {
	if err := uc.pagesReady(); err != nil {
		return nil, 0, err
	}
	return uc.pages.ListPages(ctx, tenantID, f)
}

// UpdatePageInput cambia los campos no nil. Archivar retira la pagina de su direccion publica.
type UpdatePageInput struct {
	Name    *string
	Slug    *string
	NoIndex *bool
	Status  *string
}

func (uc *UseCase) UpdatePage(ctx context.Context, tenantID, id uuid.UUID, in UpdatePageInput) (*domain.LandingDetail, error) {
	if err := uc.pagesReady(); err != nil {
		return nil, err
	}
	if in.Name == nil && in.Slug == nil && in.NoIndex == nil && in.Status == nil {
		return nil, domain.ErrNothingToUpdate
	}
	err := uc.tx.Transact(ctx, func(ctx context.Context) error {
		p, err := uc.pages.GetPageForUpdate(ctx, tenantID, id)
		if err != nil {
			return err
		}
		if in.Name != nil {
			if p.Name, err = domain.NormalizePageName(*in.Name); err != nil {
				return err
			}
		}
		if in.Slug != nil {
			if p.Slug, err = domain.NormalizePageSlug(*in.Slug); err != nil {
				return err
			}
		}
		if in.NoIndex != nil {
			p.NoIndex = *in.NoIndex
		}
		if in.Status != nil {
			if !contains(domain.PageStatuses(), *in.Status) {
				return domain.ErrInvalidPage
			}
			p.Status = *in.Status
		}
		return uc.pages.UpdatePage(ctx, p)
	})
	if err != nil {
		return nil, err
	}
	return uc.GetPage(ctx, tenantID, id)
}

// DeletePage borra una pagina archivada con sus versiones.
func (uc *UseCase) DeletePage(ctx context.Context, tenantID, id uuid.UUID) error {
	if err := uc.pagesReady(); err != nil {
		return err
	}
	return uc.tx.Transact(ctx, func(ctx context.Context) error {
		p, err := uc.pages.GetPageForUpdate(ctx, tenantID, id)
		if err != nil {
			return err
		}
		if p.Status != domain.PageStatusArchived {
			return domain.ErrPageNotArchived
		}
		return uc.pages.DeletePage(ctx, tenantID, id)
	})
}

// CreatePageVersion guarda un borrador nuevo: las versiones no se reescriben.
func (uc *UseCase) CreatePageVersion(ctx context.Context, tenantID, pageID, userID uuid.UUID, in PageContentInput) (*domain.LandingVersion, error) {
	if err := uc.pagesReady(); err != nil {
		return nil, err
	}
	if userID == uuid.Nil {
		return nil, domain.ErrMissingCreator
	}
	content, err := preparePageContent(in)
	if err != nil {
		return nil, err
	}
	var v *domain.LandingVersion
	err = uc.tx.Transact(ctx, func(ctx context.Context) error {
		p, err := uc.pages.GetPageForUpdate(ctx, tenantID, pageID)
		if err != nil {
			return err
		}
		if p.Status == domain.PageStatusArchived {
			return domain.ErrPageArchived
		}
		max, err := uc.pages.MaxPageVersion(ctx, tenantID, pageID)
		if err != nil {
			return err
		}
		v = newPageVersion(p, max+1, content, userID)
		return uc.pages.CreatePageVersion(ctx, v)
	})
	if err != nil {
		return nil, err
	}
	return v, nil
}

func (uc *UseCase) GetPageVersion(ctx context.Context, tenantID, pageID uuid.UUID, n int) (*domain.LandingVersion, error) {
	if err := uc.pagesReady(); err != nil {
		return nil, err
	}
	if _, err := uc.pages.GetPage(ctx, tenantID, pageID); err != nil {
		return nil, err
	}
	return uc.pages.GetPageVersion(ctx, tenantID, pageID, n)
}

// PublishPageVersion pone la version en la direccion publica; la anterior queda supersedida.
func (uc *UseCase) PublishPageVersion(ctx context.Context, tenantID, pageID uuid.UUID, n int) (*domain.LandingDetail, error) {
	if err := uc.pagesReady(); err != nil {
		return nil, err
	}
	err := uc.tx.Transact(ctx, func(ctx context.Context) error {
		p, err := uc.pages.GetPageForUpdate(ctx, tenantID, pageID)
		if err != nil {
			return err
		}
		if p.Status == domain.PageStatusArchived {
			return domain.ErrPageArchived
		}
		v, err := uc.pages.GetPageVersion(ctx, tenantID, pageID, n)
		if err != nil {
			return err
		}
		if v.Status == domain.VersionStatusPublished {
			return domain.ErrVersionAlreadyPublished
		}
		if err := uc.pages.SupersedePublishedPage(ctx, tenantID, pageID); err != nil {
			return err
		}
		if err := uc.pages.MarkPageVersionPublished(ctx, tenantID, v.ID); err != nil {
			return err
		}
		p.CurrentVersion = n
		return uc.pages.UpdatePage(ctx, p)
	})
	if err != nil {
		return nil, err
	}
	return uc.GetPage(ctx, tenantID, pageID)
}

// UnpublishPage retira la pagina de su direccion publica sin borrar ninguna version.
func (uc *UseCase) UnpublishPage(ctx context.Context, tenantID, pageID uuid.UUID) (*domain.LandingDetail, error) {
	if err := uc.pagesReady(); err != nil {
		return nil, err
	}
	err := uc.tx.Transact(ctx, func(ctx context.Context) error {
		p, err := uc.pages.GetPageForUpdate(ctx, tenantID, pageID)
		if err != nil {
			return err
		}
		if p.CurrentVersion == 0 {
			return domain.ErrPageNotPublished
		}
		if err := uc.pages.SupersedePublishedPage(ctx, tenantID, pageID); err != nil {
			return err
		}
		p.CurrentVersion = 0
		return uc.pages.UpdatePage(ctx, p)
	})
	if err != nil {
		return nil, err
	}
	return uc.GetPage(ctx, tenantID, pageID)
}

// PublishedPage es el documento que se sirve en la direccion publica.
type PublishedPage struct {
	Body    []byte
	NoIndex bool
	CSP     string
}

// ServePage devuelve la version publicada de la pagina activa con ese slug; cualquier otra
// cosa (sin publicar, archivada, inexistente) es ErrPageNotFound.
func (uc *UseCase) ServePage(ctx context.Context, tenantID uuid.UUID, slug string) (*PublishedPage, error) {
	if err := uc.pagesReady(); err != nil {
		return nil, err
	}
	if _, err := domain.NormalizePageSlug(slug); err != nil {
		return nil, domain.ErrPageNotFound
	}
	p, err := uc.pages.GetPageBySlug(ctx, tenantID, slug)
	if err != nil {
		return nil, err
	}
	if p.Status != domain.PageStatusActive || p.CurrentVersion == 0 {
		return nil, domain.ErrPageNotFound
	}
	v, err := uc.pages.GetPageVersion(ctx, tenantID, p.ID, p.CurrentVersion)
	if errors.Is(err, domain.ErrPageVersionNotFound) {
		return nil, domain.ErrPageNotFound
	}
	if err != nil {
		return nil, err
	}
	out := &PublishedPage{NoIndex: p.NoIndex, CSP: landing.CSP(uc.platformOrigin)}
	key := renderedKey{version: v.ID, noIndex: p.NoIndex}
	if body, ok := uc.rendered.get(key); ok {
		out.Body = body
		return out, nil
	}
	body, err := landing.Document(landing.DocumentInput{
		TenantID: tenantID, Title: v.Content.Title, Description: v.Content.Description, NoIndex: p.NoIndex,
		HTML: v.Content.HTML, CSS: v.Content.CSS, PublicBaseURL: uc.publicBaseURL,
	})
	if err != nil {
		return nil, err
	}
	uc.rendered.put(key, body)
	out.Body = body
	return out, nil
}
