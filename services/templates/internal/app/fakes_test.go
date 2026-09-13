package app

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/services/templates/internal/domain"
	"github.com/alonsosss/corforce-email/services/templates/internal/ports"
	"github.com/google/uuid"
)

// fakeRepo guarda plantillas y versiones en memoria con la misma semantica que el
// repositorio real: nombre unico por empresa y una sola version publicada por plantilla.
type fakeRepo struct {
	templates map[uuid.UUID]*domain.Template
	versions  []*domain.Version
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{templates: map[uuid.UUID]*domain.Template{}}
}

func (f *fakeRepo) CreateTemplate(_ context.Context, t *domain.Template) error {
	for _, existing := range f.templates {
		if existing.TenantID == t.TenantID && existing.Name == t.Name {
			return domain.ErrTemplateNameTaken
		}
	}
	t.CreatedAt, t.UpdatedAt = time.Now(), time.Now()
	copied := *t
	f.templates[t.ID] = &copied
	return nil
}

func (f *fakeRepo) GetTemplate(_ context.Context, tenantID, id uuid.UUID) (*domain.Template, error) {
	t, ok := f.templates[id]
	if !ok || t.TenantID != tenantID {
		return nil, domain.ErrTemplateNotFound
	}
	copied := *t
	return &copied, nil
}

func (f *fakeRepo) GetTemplateForUpdate(ctx context.Context, tenantID, id uuid.UUID) (*domain.Template, error) {
	return f.GetTemplate(ctx, tenantID, id)
}

func (f *fakeRepo) ListTemplates(_ context.Context, tenantID uuid.UUID, filter ports.ListFilter) ([]*domain.Template, int64, error) {
	var out []*domain.Template
	for _, t := range f.templates {
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

func (f *fakeRepo) UpdateTemplate(_ context.Context, t *domain.Template) error {
	existing, ok := f.templates[t.ID]
	if !ok {
		return domain.ErrTemplateNotFound
	}
	for _, other := range f.templates {
		if other.ID != t.ID && other.TenantID == t.TenantID && other.Name == t.Name {
			return domain.ErrTemplateNameTaken
		}
	}
	*existing = *t
	existing.UpdatedAt = time.Now()
	return nil
}

func (f *fakeRepo) DeleteTemplate(_ context.Context, tenantID, id uuid.UUID) error {
	t, ok := f.templates[id]
	if !ok || t.TenantID != tenantID {
		return domain.ErrTemplateNotFound
	}
	delete(f.templates, id)
	kept := f.versions[:0]
	for _, v := range f.versions {
		if v.TemplateID != id {
			kept = append(kept, v)
		}
	}
	f.versions = kept
	return nil
}

func (f *fakeRepo) CreateVersion(_ context.Context, v *domain.Version) error {
	v.CreatedAt = time.Now()
	copied := *v
	f.versions = append(f.versions, &copied)
	return nil
}

func (f *fakeRepo) GetVersion(_ context.Context, tenantID, templateID uuid.UUID, version int) (*domain.Version, error) {
	for _, v := range f.versions {
		if v.TenantID == tenantID && v.TemplateID == templateID && v.Version == version {
			copied := *v
			return &copied, nil
		}
	}
	return nil, domain.ErrVersionNotFound
}

func (f *fakeRepo) GetPublishedVersion(_ context.Context, tenantID, templateID uuid.UUID) (*domain.Version, error) {
	for _, v := range f.versions {
		if v.TenantID == tenantID && v.TemplateID == templateID && v.Status == domain.VersionStatusPublished {
			copied := *v
			return &copied, nil
		}
	}
	return nil, domain.ErrVersionNotFound
}

func (f *fakeRepo) ListVersions(_ context.Context, tenantID, templateID uuid.UUID) ([]domain.VersionSummary, error) {
	out := make([]domain.VersionSummary, 0)
	for _, v := range f.versions {
		if v.TenantID == tenantID && v.TemplateID == templateID {
			out = append(out, domain.VersionSummary{ID: v.ID, Version: v.Version, Status: v.Status, PublishedAt: v.PublishedAt, CreatedBy: v.CreatedBy, CreatedAt: v.CreatedAt})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version > out[j].Version })
	return out, nil
}

func (f *fakeRepo) MaxVersion(_ context.Context, tenantID, templateID uuid.UUID) (int, error) {
	max := 0
	for _, v := range f.versions {
		if v.TenantID == tenantID && v.TemplateID == templateID && v.Version > max {
			max = v.Version
		}
	}
	return max, nil
}

func (f *fakeRepo) SupersedePublished(_ context.Context, tenantID, templateID uuid.UUID) error {
	for _, v := range f.versions {
		if v.TenantID == tenantID && v.TemplateID == templateID && v.Status == domain.VersionStatusPublished {
			v.Status = domain.VersionStatusSuperseded
		}
	}
	return nil
}

func (f *fakeRepo) MarkPublished(_ context.Context, tenantID, versionID uuid.UUID) error {
	for _, v := range f.versions {
		if v.TenantID == tenantID && v.ID == versionID {
			now := time.Now()
			v.Status, v.PublishedAt = domain.VersionStatusPublished, &now
			return nil
		}
	}
	return domain.ErrVersionNotFound
}

// publishedCount cuenta las versiones publicadas de una plantilla (el invariante).
func (f *fakeRepo) publishedCount(templateID uuid.UUID) int {
	n := 0
	for _, v := range f.versions {
		if v.TemplateID == templateID && v.Status == domain.VersionStatusPublished {
			n++
		}
	}
	return n
}

// fakeTx ejecuta fn sin transaccion real.
type fakeTx struct{ calls int }

func (f *fakeTx) Transact(ctx context.Context, fn func(ctx context.Context) error) error {
	f.calls++
	return fn(ctx)
}

// fakeRenderer acepta todo salvo el contenido que contenga "INVALID" y renderiza una
// concatenacion trivial; el motor real tiene sus propias pruebas.
type fakeRenderer struct{ compiled int }

type fakeCompiled struct{ content domain.Content }

func (f *fakeRenderer) Compile(c domain.Content) (ports.CompiledTemplate, error) {
	f.compiled++
	if strings.Contains(c.HTML, "INVALID") {
		return nil, domain.ErrInvalidTemplate
	}
	return &fakeCompiled{content: c}, nil
}

func (f *fakeCompiled) Render(values map[string]any) (domain.Rendered, error) {
	name, _ := values["name"].(string)
	return domain.Rendered{Subject: f.content.Subject + "|" + name, HTML: f.content.HTML, Text: "texto"}, nil
}

type publishedEvent struct {
	TenantID, TemplateID uuid.UUID
	Version              int
}

type fakeEvents struct{ published []publishedEvent }

func (f *fakeEvents) TemplatePublished(_ context.Context, tenantID, templateID uuid.UUID, version int) error {
	f.published = append(f.published, publishedEvent{tenantID, templateID, version})
	return nil
}
