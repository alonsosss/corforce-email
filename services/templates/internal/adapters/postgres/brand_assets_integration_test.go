//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/templates/internal/domain"
	"github.com/alonsosss/corforce-email/services/templates/internal/ports"
	"github.com/google/uuid"
)

func TestElEditorSeGuardaYNoCambiaTrasPublicar(t *testing.T) {
	ctx, repo, _, pool, tenantID := setup(t)
	tpl := newTemplate(tenantID, "Editor")
	if err := repo.CreateTemplate(ctx, tpl); err != nil {
		t.Fatal(err)
	}
	v := newVersion(tpl, 1)
	v.Editor = &domain.EditorDocument{Kind: domain.EditorKindGrapesJSMJML, Project: json.RawMessage(`{"pages":[]}`), MJML: "<mjml/>"}
	if err := repo.CreateVersion(ctx, v); err != nil {
		t.Fatal(err)
	}
	plain := newVersion(tpl, 2)
	if err := repo.CreateVersion(ctx, plain); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetVersion(ctx, tenantID, tpl.ID, 1)
	if err != nil || got.Editor == nil || string(got.Editor.Project) != `{"pages": []}` && string(got.Editor.Project) != `{"pages":[]}` || got.Editor.MJML != "<mjml/>" {
		t.Fatalf("editor leido: %+v %v", got.Editor, err)
	}
	if got, _ := repo.GetVersion(ctx, tenantID, tpl.ID, 2); got.Editor != nil {
		t.Fatalf("sin editor es NULL: %+v", got.Editor)
	}

	if err := repo.MarkPublished(ctx, tenantID, v.ID); err != nil {
		t.Fatalf("publicar cambia solo el estado: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE templates.versions SET editor = '{}' WHERE id = $1`, v.ID); err == nil {
		t.Fatal("el editor de una version publicada no cambia")
	}
	if _, err := pool.Exec(context.Background(), `UPDATE templates.versions SET html = '<p>otro</p>' WHERE id = $1`, v.ID); err == nil {
		t.Fatal("el HTML de una version publicada no cambia")
	}
	if _, err := pool.Exec(context.Background(), `UPDATE templates.versions SET editor = '[]' WHERE id = $1`, plain.ID); err == nil {
		t.Fatal("el editor es un objeto")
	}
}

func TestKitDeMarcaPorEmpresa(t *testing.T) {
	ctx, repo, _, pool, tenantID := setup(t)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM templates.brand_kits WHERE tenant_id = $1`, tenantID)
	})
	if k, err := repo.GetBrandKit(ctx, tenantID); k != nil || err != nil {
		t.Fatalf("sin kit: %+v %v", k, err)
	}
	logo := uuid.New()
	kit := &domain.BrandKit{
		TenantID: tenantID, LogoAssetID: &logo, Colors: []string{"#0B5FFF"}, Fonts: []string{"Inter", "Georgia"},
		Footer:    domain.BrandFooter{Company: "Acme SAC", Address: "Av. Siempre Viva 742", Website: "https://acme.pe", SupportEmail: "hola@acme.pe"},
		UpdatedBy: uuid.New(),
	}
	if err := repo.UpsertBrandKit(ctx, kit); err != nil || kit.UpdatedAt == nil {
		t.Fatalf("alta: %v", err)
	}
	kit.LogoAssetID, kit.Colors, kit.Fonts = nil, []string{}, []string{"Arial"}
	if err := repo.UpsertBrandKit(ctx, kit); err != nil {
		t.Fatalf("reemplazo: %v", err)
	}
	got, err := repo.GetBrandKit(ctx, tenantID)
	if err != nil || got.LogoAssetID != nil || len(got.Colors) != 0 || strings.Join(got.Fonts, ",") != "Arial" || got.Footer.Address != "Av. Siempre Viva 742" {
		t.Fatalf("kit leido: %+v %v", got, err)
	}
	if other, _ := repo.GetBrandKit(ctx, uuid.New()); other != nil {
		t.Fatal("otra empresa no ve el kit")
	}
}

func TestImagenesPorContenidoYBorradoLogico(t *testing.T) {
	ctx, repo, _, pool, tenantID := setup(t)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM templates.assets WHERE tenant_id = $1`, tenantID)
	})
	newAsset := func(sha string) *domain.Asset {
		return &domain.Asset{
			ID: uuid.New(), TenantID: tenantID, SHA256: sha, ObjectKey: domain.AssetObjectKey(tenantID, sha, "png"),
			ContentType: "image/png", SizeBytes: 10, Width: 2, Height: 2, Name: "a.png", CreatedBy: uuid.New(),
		}
	}
	shas := []string{strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)}
	var assets []*domain.Asset
	for _, sha := range shas {
		a := newAsset(sha)
		if err := repo.CreateAsset(ctx, a); err != nil {
			t.Fatal(err)
		}
		assets = append(assets, a)
		time.Sleep(2 * time.Millisecond)
	}
	if err := repo.CreateAsset(ctx, newAsset(shas[0])); !errors.Is(err, domain.ErrAssetExists) {
		t.Fatalf("mismo contenido: %v", err)
	}
	if err := repo.CreateAsset(ctx, newAsset("NO-ES-HEX")); err == nil {
		t.Fatal("el sha256 lo comprueba la base")
	}

	first, err := repo.ListAssets(ctx, tenantID, nil, 2)
	if err != nil || len(first) != 2 || first[0].ID != assets[2].ID {
		t.Fatalf("primera pagina: %v %v", first, err)
	}
	last := first[1]
	rest, err := repo.ListAssets(ctx, tenantID, &ports.AssetCursor{CreatedAt: last.CreatedAt, ID: last.ID}, 2)
	if err != nil || len(rest) != 1 || rest[0].ID != assets[0].ID {
		t.Fatalf("segunda pagina: %v %v", rest, err)
	}

	if err := repo.DeleteAsset(ctx, tenantID, assets[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteAsset(ctx, tenantID, assets[0].ID); !errors.Is(err, domain.ErrAssetNotFound) {
		t.Fatalf("borrar dos veces: %v", err)
	}
	if _, err := repo.GetAsset(ctx, tenantID, assets[0].ID); !errors.Is(err, domain.ErrAssetNotFound) {
		t.Fatalf("una retirada no se lee: %v", err)
	}
	a, deleted, err := repo.GetAssetBySHA(ctx, tenantID, shas[0])
	if err != nil || !deleted || a.ID != assets[0].ID {
		t.Fatalf("por contenido: %v %v", deleted, err)
	}
	restored, err := repo.RestoreAsset(ctx, tenantID, a.ID, "vuelta.png")
	if err != nil || restored.Name != "vuelta.png" {
		t.Fatalf("reactivar: %+v %v", restored, err)
	}
	if _, _, err := repo.GetAssetBySHA(ctx, uuid.New(), shas[0]); !errors.Is(err, domain.ErrAssetNotFound) {
		t.Fatalf("otra empresa: %v", err)
	}
}
