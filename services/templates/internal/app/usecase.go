package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/alonsosss/corforce-email/services/templates/internal/domain"
	"github.com/alonsosss/corforce-email/services/templates/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	defaultPageSize = 20
	maxPageSize     = 100
)

// NormalizePage acota la paginacion de los listados.
func NormalizePage(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = defaultPageSize
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}
	return page, pageSize
}

// Deps agrupa los puertos del caso de uso. Un constructor con nombres evita cruzar dos
// argumentos posicionales del mismo tipo.
type Deps struct {
	Repo     ports.Repository
	Tx       ports.Transactor
	Renderer ports.Renderer
	Events   ports.EventPublisher
	Logger   *zap.Logger
}

type UseCase struct {
	repo     ports.Repository
	tx       ports.Transactor
	renderer ports.Renderer
	events   ports.EventPublisher
	logger   *zap.Logger
}

func New(d Deps) *UseCase {
	logger := d.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	return &UseCase{repo: d.Repo, tx: d.Tx, renderer: d.Renderer, events: d.Events, logger: logger}
}

// CreateTemplateInput es el alta de una plantilla con su version 1 en borrador.
type CreateTemplateInput struct {
	Name        string
	Description string
	Kind        string
	Content     domain.Content
}

func (uc *UseCase) CreateTemplate(ctx context.Context, tenantID, userID uuid.UUID, in CreateTemplateInput) (*domain.Template, *domain.Version, error) {
	if userID == uuid.Nil {
		return nil, nil, domain.ErrMissingCreator
	}
	name, err := normalizeName(in.Name)
	if err != nil {
		return nil, nil, err
	}
	if !contains(domain.Kinds(), in.Kind) {
		return nil, nil, domain.ErrInvalidKind
	}
	content := normalizeContent(in.Content)
	if _, err := uc.renderer.Compile(content); err != nil {
		return nil, nil, err
	}

	t := &domain.Template{
		ID:          uuid.New(),
		TenantID:    tenantID,
		Name:        name,
		Description: strings.TrimSpace(in.Description),
		Kind:        in.Kind,
		Status:      domain.TemplateStatusActive,
		CreatedBy:   userID,
	}
	v := newVersion(t, 1, content, userID)
	err = uc.tx.Transact(ctx, func(ctx context.Context) error {
		if err := uc.repo.CreateTemplate(ctx, t); err != nil {
			return err
		}
		return uc.repo.CreateVersion(ctx, v)
	})
	if err != nil {
		return nil, nil, err
	}
	return t, v, nil
}

func (uc *UseCase) GetTemplate(ctx context.Context, tenantID, id uuid.UUID) (*domain.TemplateDetail, error) {
	t, err := uc.repo.GetTemplate(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	detail := &domain.TemplateDetail{Template: t}
	if t.CurrentVersion > 0 {
		current, err := uc.repo.GetVersion(ctx, tenantID, id, t.CurrentVersion)
		if err != nil {
			return nil, err
		}
		detail.Current = current
	}
	if detail.Versions, err = uc.repo.ListVersions(ctx, tenantID, id); err != nil {
		return nil, err
	}
	return detail, nil
}

func (uc *UseCase) ListTemplates(ctx context.Context, tenantID uuid.UUID, f ports.ListFilter) ([]*domain.Template, int64, error) {
	if f.Kind != "" && !contains(domain.Kinds(), f.Kind) {
		return nil, 0, domain.ErrInvalidKind
	}
	if f.Status != "" && !contains(domain.TemplateStatuses(), f.Status) {
		return nil, 0, domain.ErrInvalidTemplateStatus
	}
	f.Search = strings.TrimSpace(f.Search)
	return uc.repo.ListTemplates(ctx, tenantID, f)
}

// UpdateTemplateInput: los campos nil no cambian.
type UpdateTemplateInput struct {
	Name        *string
	Description *string
	Status      *string
}

func (uc *UseCase) UpdateTemplate(ctx context.Context, tenantID, id uuid.UUID, in UpdateTemplateInput) (*domain.Template, error) {
	if in.Name == nil && in.Description == nil && in.Status == nil {
		return nil, domain.ErrNothingToUpdate
	}
	if in.Status != nil && !contains(domain.TemplateStatuses(), *in.Status) {
		return nil, domain.ErrInvalidTemplateStatus
	}
	var name string
	if in.Name != nil {
		var err error
		if name, err = normalizeName(*in.Name); err != nil {
			return nil, err
		}
	}
	var updated *domain.Template
	err := uc.tx.Transact(ctx, func(ctx context.Context) error {
		t, err := uc.repo.GetTemplateForUpdate(ctx, tenantID, id)
		if err != nil {
			return err
		}
		if in.Name != nil {
			t.Name = name
		}
		if in.Description != nil {
			t.Description = strings.TrimSpace(*in.Description)
		}
		if in.Status != nil {
			t.Status = *in.Status
		}
		if err := uc.repo.UpdateTemplate(ctx, t); err != nil {
			return err
		}
		updated = t
		return nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// DeleteTemplate borra la plantilla y sus versiones. Solo se admite sobre una plantilla
// archivada: archivar es la confirmacion explicita.
func (uc *UseCase) DeleteTemplate(ctx context.Context, tenantID, id uuid.UUID) error {
	return uc.tx.Transact(ctx, func(ctx context.Context) error {
		t, err := uc.repo.GetTemplateForUpdate(ctx, tenantID, id)
		if err != nil {
			return err
		}
		if t.Status != domain.TemplateStatusArchived {
			return domain.ErrTemplateNotArchived
		}
		return uc.repo.DeleteTemplate(ctx, tenantID, id)
	})
}

func (uc *UseCase) ListVersions(ctx context.Context, tenantID, templateID uuid.UUID) ([]domain.VersionSummary, error) {
	if _, err := uc.repo.GetTemplate(ctx, tenantID, templateID); err != nil {
		return nil, err
	}
	return uc.repo.ListVersions(ctx, tenantID, templateID)
}

func (uc *UseCase) GetVersion(ctx context.Context, tenantID, templateID uuid.UUID, version int) (*domain.Version, error) {
	return uc.repo.GetVersion(ctx, tenantID, templateID, version)
}

// CreateVersion anade un borrador con el siguiente numero. El bloqueo de la plantilla
// serializa la numeracion entre peticiones concurrentes.
func (uc *UseCase) CreateVersion(ctx context.Context, tenantID, templateID, userID uuid.UUID, content domain.Content) (*domain.Version, error) {
	if userID == uuid.Nil {
		return nil, domain.ErrMissingCreator
	}
	content = normalizeContent(content)
	if _, err := uc.renderer.Compile(content); err != nil {
		return nil, err
	}
	var v *domain.Version
	err := uc.tx.Transact(ctx, func(ctx context.Context) error {
		t, err := uc.repo.GetTemplateForUpdate(ctx, tenantID, templateID)
		if err != nil {
			return err
		}
		if t.Status == domain.TemplateStatusArchived {
			return domain.ErrTemplateArchived
		}
		last, err := uc.repo.MaxVersion(ctx, tenantID, templateID)
		if err != nil {
			return err
		}
		v = newVersion(t, last+1, content, userID)
		return uc.repo.CreateVersion(ctx, v)
	})
	if err != nil {
		return nil, err
	}
	return v, nil
}

// PublishVersion deja una sola version publicada: la anterior pasa a supersedida, la
// plantilla apunta a la nueva y el evento sale por la outbox en la misma transaccion.
func (uc *UseCase) PublishVersion(ctx context.Context, tenantID, templateID uuid.UUID, version int) (*domain.Version, error) {
	var published *domain.Version
	err := uc.tx.Transact(ctx, func(ctx context.Context) error {
		t, err := uc.repo.GetTemplateForUpdate(ctx, tenantID, templateID)
		if err != nil {
			return err
		}
		if t.Status == domain.TemplateStatusArchived {
			return domain.ErrTemplateArchived
		}
		v, err := uc.repo.GetVersion(ctx, tenantID, templateID, version)
		if err != nil {
			return err
		}
		if v.Status == domain.VersionStatusPublished {
			return domain.ErrVersionAlreadyPublished
		}
		if err := uc.repo.SupersedePublished(ctx, tenantID, templateID); err != nil {
			return err
		}
		if err := uc.repo.MarkPublished(ctx, tenantID, v.ID); err != nil {
			return err
		}
		t.CurrentVersion = version
		if err := uc.repo.UpdateTemplate(ctx, t); err != nil {
			return err
		}
		if published, err = uc.repo.GetVersion(ctx, tenantID, templateID, version); err != nil {
			return err
		}
		return uc.events.TemplatePublished(ctx, tenantID, templateID, version)
	})
	if err != nil {
		return nil, err
	}
	uc.logger.Info("version publicada",
		zap.String("tenant_id", tenantID.String()),
		zap.String("template_id", templateID.String()),
		zap.Int("version", version))
	return published, nil
}

// RenderInput es una peticion de renderizado. Sin Version se usa la publicada. Preview
// admite borradores y plantillas archivadas; un renderizado real, no.
type RenderInput struct {
	Version  *int
	Values   map[string]json.RawMessage
	Reserved map[string]string
	Preview  bool
}

func (uc *UseCase) Render(ctx context.Context, tenantID, templateID uuid.UUID, in RenderInput) (*domain.Rendered, error) {
	t, err := uc.repo.GetTemplate(ctx, tenantID, templateID)
	if err != nil {
		return nil, err
	}
	if !in.Preview && t.Status == domain.TemplateStatusArchived {
		return nil, domain.ErrTemplateArchived
	}
	var v *domain.Version
	if in.Version != nil {
		if v, err = uc.repo.GetVersion(ctx, tenantID, templateID, *in.Version); err != nil {
			return nil, err
		}
		if !in.Preview && !v.WasPublished() {
			return nil, domain.ErrVersionNotPublished
		}
	} else {
		if v, err = uc.repo.GetPublishedVersion(ctx, tenantID, templateID); err != nil {
			if errors.Is(err, domain.ErrVersionNotFound) {
				return nil, domain.ErrNoPublishedVersion
			}
			return nil, err
		}
	}
	compiled, err := uc.renderer.Compile(domain.Content{
		Subject: v.Subject, HTML: v.HTML, Text: v.Text, Variables: v.Variables,
	})
	if err != nil {
		return nil, err
	}
	values, err := domain.ResolveValues(v.Variables, in.Values, in.Reserved)
	if err != nil {
		return nil, err
	}
	out, err := compiled.Render(values)
	if err != nil {
		return nil, err
	}
	out.Version = v.Version
	return &out, nil
}

func newVersion(t *domain.Template, number int, c domain.Content, userID uuid.UUID) *domain.Version {
	return &domain.Version{
		ID:         uuid.New(),
		TenantID:   t.TenantID,
		TemplateID: t.ID,
		Version:    number,
		Subject:    c.Subject,
		HTML:       c.HTML,
		Text:       c.Text,
		Variables:  c.Variables,
		Status:     domain.VersionStatusDraft,
		CreatedBy:  userID,
	}
}

// normalizeContent garantiza una lista de variables no nula (se guarda como [] en jsonb)
// y descarta un texto en blanco, que equivale a "generar desde el HTML".
func normalizeContent(c domain.Content) domain.Content {
	if c.Variables == nil {
		c.Variables = []domain.Variable{}
	}
	if c.Text != nil && strings.TrimSpace(*c.Text) == "" {
		c.Text = nil
	}
	return c
}

func normalizeName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > domain.MaxNameLength {
		return "", domain.ErrInvalidName
	}
	return name, nil
}

func contains(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}
