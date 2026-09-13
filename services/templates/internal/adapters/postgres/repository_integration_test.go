//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	outboxadapter "github.com/alonsosss/corforce-email/services/templates/internal/adapters/outbox"
	"github.com/alonsosss/corforce-email/services/templates/internal/domain"
	"github.com/alonsosss/corforce-email/services/templates/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TEMPLATES_TEST_DSN apunta a una base con migrations/tenant/canonical/platform/00_outbox.sql
// y migrations/tenant/canonical/templates/01_templates.sql aplicadas.
func setup(t *testing.T) (context.Context, *Repository, *db.ContextPool, *pgxpool.Pool, uuid.UUID) {
	t.Helper()
	dsn := os.Getenv("TEMPLATES_TEST_DSN")
	if dsn == "" {
		t.Skip("TEMPLATES_TEST_DSN no definido")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("conectar: %v", err)
	}
	tenantID := uuid.New()
	ctx := middleware.WithIdentity(db.WithPool(context.Background(), pool), uuid.New().String(), tenantID.String())
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM templates.templates WHERE tenant_id = $1`, tenantID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM platform.event_outbox WHERE tenant_id = $1`, tenantID)
		pool.Close()
	})
	ctxPool := &db.ContextPool{}
	return ctx, NewRepository(ctxPool), ctxPool, pool, tenantID
}

func newTemplate(tenantID uuid.UUID, name string) *domain.Template {
	return &domain.Template{
		ID: uuid.New(), TenantID: tenantID, Name: name, Description: "d",
		Kind: domain.KindTransactional, Status: domain.TemplateStatusActive, CreatedBy: uuid.New(),
	}
}

func newVersion(t *domain.Template, n int) *domain.Version {
	return &domain.Version{
		ID: uuid.New(), TenantID: t.TenantID, TemplateID: t.ID, Version: n,
		Subject: "Asunto {{.name}}", HTML: "<p>{{.name}}</p>",
		Variables: []domain.Variable{{Name: "name", Type: domain.VarString, Required: true}},
		Status:    domain.VersionStatusDraft, CreatedBy: t.CreatedBy,
	}
}

func TestPlantillasYVersiones(t *testing.T) {
	ctx, repo, ctxPool, _, tenantID := setup(t)

	tpl := newTemplate(tenantID, "Bienvenida")
	err := ctxPool.Transact(ctx, func(ctx context.Context) error {
		if err := repo.CreateTemplate(ctx, tpl); err != nil {
			return err
		}
		return repo.CreateVersion(ctx, newVersion(tpl, 1))
	})
	if err != nil {
		t.Fatalf("crear plantilla y version: %v", err)
	}
	if tpl.CreatedAt.IsZero() || tpl.UpdatedAt.IsZero() {
		t.Errorf("RETURNING no rellena las fechas: %+v", tpl)
	}

	// Nombre unico por empresa: misma empresa falla, otra empresa no.
	if err := repo.CreateTemplate(ctx, newTemplate(tenantID, "Bienvenida")); !errors.Is(err, domain.ErrTemplateNameTaken) {
		t.Errorf("nombre repetido: %v", err)
	}
	otherTenant := newTemplate(uuid.New(), "Bienvenida")
	if err := repo.CreateTemplate(ctx, otherTenant); err != nil {
		t.Errorf("mismo nombre en otra empresa debe admitirse: %v", err)
	}
	t.Cleanup(func() { _ = repo.DeleteTemplate(ctx, otherTenant.TenantID, otherTenant.ID) })

	// Lectura con aislamiento por empresa.
	if _, err := repo.GetTemplate(ctx, otherTenant.TenantID, tpl.ID); !errors.Is(err, domain.ErrTemplateNotFound) {
		t.Errorf("otra empresa no debe ver la plantilla: %v", err)
	}
	got, err := repo.GetTemplate(ctx, tenantID, tpl.ID)
	if err != nil || got.Name != "Bienvenida" || got.CurrentVersion != 0 {
		t.Fatalf("GetTemplate: %v %+v", err, got)
	}

	// Numeracion y contenido de versiones.
	if max, err := repo.MaxVersion(ctx, tenantID, tpl.ID); err != nil || max != 1 {
		t.Errorf("MaxVersion: %v %d", err, max)
	}
	v2 := newVersion(tpl, 2)
	text := "texto propio"
	v2.Text = &text
	if err := repo.CreateVersion(ctx, v2); err != nil {
		t.Fatalf("crear v2: %v", err)
	}
	if err := repo.CreateVersion(ctx, newVersion(tpl, 2)); err == nil {
		t.Errorf("la version 2 repetida debe fallar por UNIQUE (template_id, version)")
	}
	read, err := repo.GetVersion(ctx, tenantID, tpl.ID, 2)
	if err != nil || read.Text == nil || *read.Text != text || len(read.Variables) != 1 || read.Variables[0].Name != "name" {
		t.Fatalf("GetVersion v2: %v %+v", err, read)
	}
	v1, _ := repo.GetVersion(ctx, tenantID, tpl.ID, 1)
	if v1.Text != nil {
		t.Errorf("text NULL debe leerse como nil")
	}

	// Publicacion: una sola publicada, garantizada por el indice parcial.
	if _, err := repo.GetPublishedVersion(ctx, tenantID, tpl.ID); !errors.Is(err, domain.ErrVersionNotFound) {
		t.Errorf("sin publicada: %v", err)
	}
	if err := repo.MarkPublished(ctx, tenantID, v1.ID); err != nil {
		t.Fatalf("publicar v1: %v", err)
	}
	if err := repo.MarkPublished(ctx, tenantID, v2.ID); err == nil {
		t.Errorf("publicar una segunda sin supersedir la primera debe fallar por el indice unico")
	}
	err = ctxPool.Transact(ctx, func(ctx context.Context) error {
		if _, err := repo.GetTemplateForUpdate(ctx, tenantID, tpl.ID); err != nil {
			return err
		}
		if err := repo.SupersedePublished(ctx, tenantID, tpl.ID); err != nil {
			return err
		}
		if err := repo.MarkPublished(ctx, tenantID, v2.ID); err != nil {
			return err
		}
		tpl.CurrentVersion = 2
		if err := repo.UpdateTemplate(ctx, tpl); err != nil {
			return err
		}
		return outboxadapter.NewPublisher(ctxPool).TemplatePublished(ctx, tenantID, tpl.ID, 2)
	})
	if err != nil {
		t.Fatalf("publicar v2 con outbox: %v", err)
	}
	published, err := repo.GetPublishedVersion(ctx, tenantID, tpl.ID)
	if err != nil || published.Version != 2 || published.PublishedAt == nil {
		t.Fatalf("GetPublishedVersion: %v %+v", err, published)
	}
	v1, _ = repo.GetVersion(ctx, tenantID, tpl.ID, 1)
	if v1.Status != domain.VersionStatusSuperseded || v1.PublishedAt == nil {
		t.Errorf("v1 debe quedar supersedida con fecha: %+v", v1)
	}
	summaries, err := repo.ListVersions(ctx, tenantID, tpl.ID)
	if err != nil || len(summaries) != 2 || summaries[0].Version != 2 || summaries[1].Status != domain.VersionStatusSuperseded {
		t.Errorf("ListVersions: %v %+v", err, summaries)
	}
	got, _ = repo.GetTemplate(ctx, tenantID, tpl.ID)
	if got.CurrentVersion != 2 || !got.UpdatedAt.After(got.CreatedAt) {
		t.Errorf("current_version y trigger updated_at: %+v", got)
	}

	// Listado con filtros y busqueda con comodines escapados.
	if err := repo.CreateTemplate(ctx, &domain.Template{ID: uuid.New(), TenantID: tenantID, Name: "Promo_100%", Kind: domain.KindMarketing, Status: domain.TemplateStatusArchived, CreatedBy: uuid.New()}); err != nil {
		t.Fatal(err)
	}
	items, total, err := repo.ListTemplates(ctx, tenantID, ports.ListFilter{Limit: 10})
	if err != nil || total != 2 || len(items) != 2 || items[0].Name != "Bienvenida" {
		t.Errorf("listado completo: %v %d %+v", err, total, items)
	}
	items, total, err = repo.ListTemplates(ctx, tenantID, ports.ListFilter{Kind: domain.KindMarketing, Status: domain.TemplateStatusArchived, Limit: 10})
	if err != nil || total != 1 || items[0].Name != "Promo_100%" {
		t.Errorf("filtro kind+status: %v %d %+v", err, total, items)
	}
	items, total, err = repo.ListTemplates(ctx, tenantID, ports.ListFilter{Search: "100%", Limit: 10})
	if err != nil || total != 1 {
		t.Errorf("busqueda con comodin literal: %v %d %+v", err, total, items)
	}
	items, total, err = repo.ListTemplates(ctx, tenantID, ports.ListFilter{Search: "_", Limit: 10})
	if err != nil || total != 1 {
		t.Errorf("guion bajo debe buscarse literal: %v %d %+v", err, total, items)
	}
	items, total, err = repo.ListTemplates(ctx, tenantID, ports.ListFilter{Limit: 1, Offset: 1})
	if err != nil || total != 2 || len(items) != 1 {
		t.Errorf("paginacion: %v %d %d", err, total, len(items))
	}

	// Borrado en cascada de versiones.
	if err := repo.DeleteTemplate(ctx, tenantID, tpl.ID); err != nil {
		t.Fatalf("DeleteTemplate: %v", err)
	}
	if _, err := repo.GetVersion(ctx, tenantID, tpl.ID, 1); !errors.Is(err, domain.ErrVersionNotFound) {
		t.Errorf("las versiones deben borrarse en cascada: %v", err)
	}
	if err := repo.DeleteTemplate(ctx, tenantID, tpl.ID); !errors.Is(err, domain.ErrTemplateNotFound) {
		t.Errorf("segundo borrado: %v", err)
	}
}

func TestOutboxEncolaElEventoEnLaTransaccion(t *testing.T) {
	ctx, repo, ctxPool, pool, tenantID := setup(t)
	tpl := newTemplate(tenantID, "Con evento")
	if err := repo.CreateTemplate(ctx, tpl); err != nil {
		t.Fatal(err)
	}
	publisher := outboxadapter.NewPublisher(ctxPool)

	// Si la transaccion se deshace, el evento no existe.
	rollback := errors.New("deshacer")
	err := ctxPool.Transact(ctx, func(ctx context.Context) error {
		if err := publisher.TemplatePublished(ctx, tenantID, tpl.ID, 1); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("esperaba el error de rollback, obtuve %v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM platform.event_outbox WHERE tenant_id = $1`, tenantID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("la outbox no debe tener filas tras el rollback: %v %d", err, count)
	}

	if err := ctxPool.Transact(ctx, func(ctx context.Context) error {
		return publisher.TemplatePublished(ctx, tenantID, tpl.ID, 1)
	}); err != nil {
		t.Fatalf("encolar: %v", err)
	}
	var subject string
	var payload []byte
	if err := pool.QueryRow(ctx, `SELECT subject, payload FROM platform.event_outbox WHERE tenant_id = $1`, tenantID).Scan(&subject, &payload); err != nil {
		t.Fatalf("leer outbox: %v", err)
	}
	if subject != "templates.template.published" {
		t.Errorf("subject inesperado: %s", subject)
	}
	var evt struct {
		Type     string `json:"type"`
		Source   string `json:"source"`
		TenantID string `json:"tenant_id"`
		Data     struct {
			TemplateID string `json:"template_id"`
			Version    int    `json:"version"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &evt); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if evt.Type != subject || evt.Source != "templates-service" || evt.TenantID != tenantID.String() || evt.Data.TemplateID != tpl.ID.String() || evt.Data.Version != 1 {
		t.Errorf("sobre inesperado: %+v", evt)
	}
}
