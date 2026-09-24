//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/alonsosss/corforce-email/services/templates/internal/domain"
	"github.com/alonsosss/corforce-email/services/templates/internal/ports"
	"github.com/google/uuid"
)

func newLandingPage(tenantID uuid.UUID, name, slug string) *domain.LandingPage {
	return &domain.LandingPage{ID: uuid.New(), TenantID: tenantID, Name: name, Slug: slug,
		Status: domain.PageStatusActive, NoIndex: true, CreatedBy: uuid.New()}
}

func TestPaginasDeAterrizaje(t *testing.T) {
	ctx, repo, _, pool, tenantID := setup(t)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM templates.pages WHERE tenant_id = $1`, tenantID)
	})
	p := newLandingPage(tenantID, "Oferta", "oferta")
	if err := repo.CreatePage(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreatePage(ctx, newLandingPage(tenantID, "Oferta", "otra")); !errors.Is(err, domain.ErrPageNameTaken) {
		t.Fatalf("nombre repetido: %v", err)
	}
	if err := repo.CreatePage(ctx, newLandingPage(tenantID, "Otra", "oferta")); !errors.Is(err, domain.ErrPageSlugTaken) {
		t.Fatalf("slug repetido: %v", err)
	}
	if err := repo.CreatePage(ctx, newLandingPage(uuid.New(), "Oferta", "oferta")); err != nil {
		t.Fatalf("el mismo slug en otra empresa: %v", err)
	}

	v := &domain.LandingVersion{ID: uuid.New(), TenantID: tenantID, PageID: p.ID, Version: 1,
		Content: domain.PageContent{Title: "Oferta", HTML: "<p>Hola</p>", CSS: ".a{}",
			Editor: &domain.PageEditorDocument{Kind: domain.EditorKindGrapesJSWeb, Project: json.RawMessage(`{"pages":[]}`)}},
		Status: domain.VersionStatusDraft, CreatedBy: uuid.New()}
	if err := repo.CreatePageVersion(ctx, v); err != nil {
		t.Fatal(err)
	}
	if max, _ := repo.MaxPageVersion(ctx, tenantID, p.ID); max != 1 {
		t.Fatalf("version maxima %d", max)
	}
	if err := repo.MarkPageVersionPublished(ctx, tenantID, v.ID); err != nil {
		t.Fatal(err)
	}
	p.CurrentVersion = 1
	if err := repo.UpdatePage(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetPageBySlug(ctx, tenantID, "oferta")
	if err != nil || got.CurrentVersion != 1 {
		t.Fatalf("pagina por slug: %+v %v", got, err)
	}
	gv, err := repo.GetPageVersion(ctx, tenantID, p.ID, 1)
	if err != nil || gv.Status != domain.VersionStatusPublished || gv.PublishedAt == nil || gv.Content.Editor == nil {
		t.Fatalf("version publicada: %+v %v", gv, err)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE templates.page_versions SET html = '<p>otro</p>' WHERE id = $1`, v.ID); err == nil {
		t.Fatal("el HTML de una version publicada no cambia")
	}
	if _, err := pool.Exec(context.Background(), `UPDATE templates.pages SET slug = 'Con Espacio' WHERE id = $1`, p.ID); err == nil {
		t.Fatal("un slug no valido pasa la restriccion de la base")
	}
	if err := repo.SupersedePublishedPage(ctx, tenantID, p.ID); err != nil {
		t.Fatal(err)
	}
	if gv, _ := repo.GetPageVersion(ctx, tenantID, p.ID, 1); gv.Status != domain.VersionStatusSuperseded {
		t.Fatalf("supersedida: %s", gv.Status)
	}
	items, total, err := repo.ListPages(ctx, tenantID, ports.PageFilter{Search: "ofer", Limit: 10})
	if err != nil || total != 1 || len(items) != 1 {
		t.Fatalf("listado: %d %v", total, err)
	}
	if sums, _ := repo.ListPageVersions(ctx, tenantID, p.ID); len(sums) != 1 {
		t.Fatalf("versiones %d", len(sums))
	}
	if err := repo.DeletePage(ctx, tenantID, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetPage(ctx, tenantID, p.ID); !errors.Is(err, domain.ErrPageNotFound) {
		t.Fatalf("borrada: %v", err)
	}
}
