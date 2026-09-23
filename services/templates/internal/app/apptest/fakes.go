// Package apptest contiene dobles en memoria de los puertos del servicio. Los importan
// los tests de app y de los adaptadores HTTP para no duplicarlos.
package apptest

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/services/templates/internal/domain"
	"github.com/alonsosss/corforce-email/services/templates/internal/ports"
	"github.com/google/uuid"
)

// Repo guarda plantillas y versiones en memoria con la misma semantica que el
// repositorio real: nombre unico por empresa y una sola version publicada por plantilla.
type Repo struct {
	Templates map[uuid.UUID]*domain.Template
	Versions  []*domain.Version
}

func NewRepo() *Repo {
	return &Repo{Templates: map[uuid.UUID]*domain.Template{}}
}

func (f *Repo) CreateTemplate(_ context.Context, t *domain.Template) error {
	for _, existing := range f.Templates {
		if existing.TenantID == t.TenantID && existing.Name == t.Name {
			return domain.ErrTemplateNameTaken
		}
	}
	t.CreatedAt, t.UpdatedAt = time.Now(), time.Now()
	copied := *t
	f.Templates[t.ID] = &copied
	return nil
}

func (f *Repo) GetTemplate(_ context.Context, tenantID, id uuid.UUID) (*domain.Template, error) {
	t, ok := f.Templates[id]
	if !ok || t.TenantID != tenantID {
		return nil, domain.ErrTemplateNotFound
	}
	copied := *t
	return &copied, nil
}

func (f *Repo) GetTemplateForUpdate(ctx context.Context, tenantID, id uuid.UUID) (*domain.Template, error) {
	return f.GetTemplate(ctx, tenantID, id)
}

func (f *Repo) ListTemplates(_ context.Context, tenantID uuid.UUID, filter ports.ListFilter) ([]*domain.Template, int64, error) {
	var out []*domain.Template
	for _, t := range f.Templates {
		if t.TenantID != tenantID {
			continue
		}
		if filter.Kind != "" && t.Kind != filter.Kind {
			continue
		}
		if filter.Status != "" && t.Status != filter.Status {
			continue
		}
		if filter.Search != "" && !strings.Contains(strings.ToLower(t.Name), strings.ToLower(filter.Search)) {
			continue
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, int64(len(out)), nil
}

func (f *Repo) UpdateTemplate(_ context.Context, t *domain.Template) error {
	existing, ok := f.Templates[t.ID]
	if !ok {
		return domain.ErrTemplateNotFound
	}
	for _, other := range f.Templates {
		if other.ID != t.ID && other.TenantID == t.TenantID && other.Name == t.Name {
			return domain.ErrTemplateNameTaken
		}
	}
	*existing = *t
	existing.UpdatedAt = time.Now()
	return nil
}

func (f *Repo) DeleteTemplate(_ context.Context, tenantID, id uuid.UUID) error {
	t, ok := f.Templates[id]
	if !ok || t.TenantID != tenantID {
		return domain.ErrTemplateNotFound
	}
	delete(f.Templates, id)
	kept := f.Versions[:0]
	for _, v := range f.Versions {
		if v.TemplateID != id {
			kept = append(kept, v)
		}
	}
	f.Versions = kept
	return nil
}

func (f *Repo) CreateVersion(_ context.Context, v *domain.Version) error {
	v.CreatedAt = time.Now()
	copied := *v
	f.Versions = append(f.Versions, &copied)
	return nil
}

func (f *Repo) GetVersion(_ context.Context, tenantID, templateID uuid.UUID, version int) (*domain.Version, error) {
	for _, v := range f.Versions {
		if v.TenantID == tenantID && v.TemplateID == templateID && v.Version == version {
			copied := *v
			return &copied, nil
		}
	}
	return nil, domain.ErrVersionNotFound
}

func (f *Repo) GetPublishedVersion(_ context.Context, tenantID, templateID uuid.UUID) (*domain.Version, error) {
	for _, v := range f.Versions {
		if v.TenantID == tenantID && v.TemplateID == templateID && v.Status == domain.VersionStatusPublished {
			copied := *v
			return &copied, nil
		}
	}
	return nil, domain.ErrVersionNotFound
}

func (f *Repo) ListVersions(_ context.Context, tenantID, templateID uuid.UUID) ([]domain.VersionSummary, error) {
	out := make([]domain.VersionSummary, 0)
	for _, v := range f.Versions {
		if v.TenantID == tenantID && v.TemplateID == templateID {
			out = append(out, domain.VersionSummary{ID: v.ID, Version: v.Version, Status: v.Status, PublishedAt: v.PublishedAt, CreatedBy: v.CreatedBy, CreatedAt: v.CreatedAt})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version > out[j].Version })
	return out, nil
}

func (f *Repo) MaxVersion(_ context.Context, tenantID, templateID uuid.UUID) (int, error) {
	max := 0
	for _, v := range f.Versions {
		if v.TenantID == tenantID && v.TemplateID == templateID && v.Version > max {
			max = v.Version
		}
	}
	return max, nil
}

func (f *Repo) SupersedePublished(_ context.Context, tenantID, templateID uuid.UUID) error {
	for _, v := range f.Versions {
		if v.TenantID == tenantID && v.TemplateID == templateID && v.Status == domain.VersionStatusPublished {
			v.Status = domain.VersionStatusSuperseded
		}
	}
	return nil
}

func (f *Repo) MarkPublished(_ context.Context, tenantID, versionID uuid.UUID) error {
	for _, v := range f.Versions {
		if v.TenantID == tenantID && v.ID == versionID {
			now := time.Now()
			v.Status, v.PublishedAt = domain.VersionStatusPublished, &now
			return nil
		}
	}
	return domain.ErrVersionNotFound
}

// PublishedCount cuenta las versiones publicadas de una plantilla (el invariante).
func (f *Repo) PublishedCount(templateID uuid.UUID) int {
	n := 0
	for _, v := range f.Versions {
		if v.TemplateID == templateID && v.Status == domain.VersionStatusPublished {
			n++
		}
	}
	return n
}

// Tx ejecuta fn sin transaccion real y cuenta las llamadas.
type Tx struct{ Calls int }

func (f *Tx) Transact(ctx context.Context, fn func(ctx context.Context) error) error {
	f.Calls++
	return fn(ctx)
}

// Renderer acepta todo salvo el contenido que contenga "INVALID" y renderiza una
// concatenacion trivial; el motor real tiene sus propias pruebas. En el HTML sustituye cada
// {{.nombre}} por su valor de texto, para que la verificacion vea los enlaces renderizados.
type Renderer struct{ Compiled int }

type compiled struct{ content domain.Content }

func (f *Renderer) Compile(c domain.Content) (ports.CompiledTemplate, error) {
	f.Compiled++
	if strings.Contains(c.HTML, "INVALID") {
		return nil, domain.ErrInvalidTemplate
	}
	return &compiled{content: c}, nil
}

func (f *Renderer) CompileDraft(c domain.Content) (ports.CompiledTemplate, error) {
	return f.Compile(c)
}

func (f *compiled) Variables() []domain.Variable { return f.content.Variables }

func (f *compiled) Render(values map[string]any) (domain.Rendered, error) {
	name, _ := values["name"].(string)
	html := f.content.HTML
	for key, v := range values {
		if s, ok := v.(string); ok {
			html = strings.ReplaceAll(html, "{{."+key+"}}", s)
		}
	}
	return domain.Rendered{Subject: f.content.Subject + "|" + name, HTML: html, Text: "texto"}, nil
}

// PublishedEvent es un templates.template.published capturado.
type PublishedEvent struct {
	TenantID, TemplateID uuid.UUID
	Version              int
}

// Events captura las publicaciones en vez de encolarlas.
type Events struct{ Published []PublishedEvent }

func (f *Events) TemplatePublished(_ context.Context, tenantID, templateID uuid.UUID, version int) error {
	f.Published = append(f.Published, PublishedEvent{tenantID, templateID, version})
	return nil
}
