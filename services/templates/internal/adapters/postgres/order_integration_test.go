//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/alonsosss/corforce-email/services/templates/internal/domain"
	"github.com/google/uuid"
)

// Columnas de 05_order_key_markup_hosts.sql: clave estable, marcado de la version y servidores
// de imagen del kit.
func TestClaveMarcadoYServidoresDeImagen(t *testing.T) {
	ctx, repo, _, pool, tenantID := setup(t)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM templates.brand_kits WHERE tenant_id = $1`, tenantID)
	})

	key := "pedido.confirmado"
	tpl := newTemplate(tenantID, "Pedido")
	tpl.Key = &key
	if err := repo.CreateTemplate(ctx, tpl); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetTemplateByKey(ctx, tenantID, key)
	if err != nil || got.ID != tpl.ID || got.Key == nil || *got.Key != key {
		t.Fatalf("por clave: %+v %v", got, err)
	}
	dup := newTemplate(tenantID, "Otro")
	dup.Key = &key
	if err := repo.CreateTemplate(ctx, dup); !errors.Is(err, domain.ErrTemplateKeyTaken) {
		t.Errorf("clave repetida en la empresa: %v", err)
	}
	other := newTemplate(uuid.New(), "Pedido")
	other.Key = &key
	if err := repo.CreateTemplate(ctx, other); err != nil {
		t.Errorf("la misma clave en otra empresa: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM templates.templates WHERE id = $1`, other.ID)
	})
	sinClave := newTemplate(tenantID, "Sin clave")
	if err := repo.CreateTemplate(ctx, sinClave); err != nil {
		t.Errorf("varias plantillas sin clave: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE templates.templates SET key = 'Mal Formada' WHERE id = $1`, tpl.ID); err == nil {
		t.Error("la base rechaza una clave con otro formato")
	}
	tpl.Key = nil
	if err := repo.UpdateTemplate(ctx, tpl); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetTemplateByKey(ctx, tenantID, key); !errors.Is(err, domain.ErrTemplateNotFound) {
		t.Errorf("clave quitada: %v", err)
	}

	v := newVersion(tpl, 1)
	v.Markup = domain.MarkupOrder
	if err := repo.CreateVersion(ctx, v); err != nil {
		t.Fatal(err)
	}
	plain := newVersion(tpl, 2)
	if err := repo.CreateVersion(ctx, plain); err != nil {
		t.Fatal(err)
	}
	if got, err := repo.GetVersion(ctx, tenantID, tpl.ID, 1); err != nil || got.Markup != domain.MarkupOrder {
		t.Fatalf("marcado leido: %+v %v", got, err)
	}
	if got, _ := repo.GetVersion(ctx, tenantID, tpl.ID, 2); got.Markup != "" {
		t.Fatalf("sin marcado es vacio: %q", got.Markup)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE templates.versions SET markup = 'factura' WHERE id = $1`, plain.ID); err == nil {
		t.Error("la base rechaza un marcado desconocido")
	}
	if err := repo.MarkPublished(ctx, tenantID, v.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE templates.versions SET markup = NULL WHERE id = $1`, v.ID); err == nil {
		t.Error("el marcado de una version publicada no cambia")
	}

	kit := &domain.BrandKit{TenantID: tenantID, Colors: []string{}, Fonts: []string{}, UpdatedBy: uuid.New()}
	if err := repo.UpsertBrandKit(ctx, kit); err != nil {
		t.Fatalf("un kit sin servidores: %v", err)
	}
	kit.ImageHosts = []string{"tienda.example", "cdn.otra.example"}
	if err := repo.UpsertBrandKit(ctx, kit); err != nil {
		t.Fatal(err)
	}
	if got, err := repo.GetBrandKit(ctx, tenantID); err != nil || len(got.ImageHosts) != 2 || got.ImageHosts[1] != "cdn.otra.example" {
		t.Fatalf("servidores leidos: %+v %v", got, err)
	}
}
