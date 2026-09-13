package app

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/alonsosss/corforce-email/services/templates/internal/domain"
	"github.com/alonsosss/corforce-email/services/templates/internal/ports"
	"github.com/google/uuid"
)

type harness struct {
	uc       *UseCase
	repo     *fakeRepo
	tx       *fakeTx
	renderer *fakeRenderer
	events   *fakeEvents
	tenant   uuid.UUID
	user     uuid.UUID
}

func newHarness() *harness {
	h := &harness{repo: newFakeRepo(), tx: &fakeTx{}, renderer: &fakeRenderer{}, events: &fakeEvents{}, tenant: uuid.New(), user: uuid.New()}
	h.uc = New(Deps{Repo: h.repo, Tx: h.tx, Renderer: h.renderer, Events: h.events})
	return h
}

func content(html string) domain.Content {
	return domain.Content{Subject: "Asunto", HTML: html, Variables: []domain.Variable{{Name: "name", Type: domain.VarString}}}
}

func (h *harness) create(t *testing.T, name string) *domain.Template {
	t.Helper()
	tpl, v, err := h.uc.CreateTemplate(context.Background(), h.tenant, h.user, CreateTemplateInput{
		Name: name, Kind: domain.KindTransactional, Content: content("<p>{{.name}}</p>"),
	})
	if err != nil {
		t.Fatalf("CreateTemplate: %v", err)
	}
	if v.Version != 1 || v.Status != domain.VersionStatusDraft || tpl.CurrentVersion != 0 {
		t.Fatalf("la version 1 debe nacer en borrador sin publicada: %+v %+v", tpl, v)
	}
	return tpl
}

func TestCreateTemplateCompilaYGuardaEnTransaccion(t *testing.T) {
	h := newHarness()
	tpl := h.create(t, "  Bienvenida  ")
	if tpl.Name != "Bienvenida" {
		t.Errorf("el nombre no se normalizo: %q", tpl.Name)
	}
	if h.renderer.compiled != 1 || h.tx.calls != 1 {
		t.Errorf("esperaba una compilacion y una transaccion, hubo %d y %d", h.renderer.compiled, h.tx.calls)
	}
	if _, _, err := h.uc.CreateTemplate(context.Background(), h.tenant, h.user, CreateTemplateInput{
		Name: "bienvenida2", Kind: domain.KindMarketing, Content: content("<p>INVALID</p>"),
	}); !errors.Is(err, domain.ErrInvalidTemplate) {
		t.Errorf("una plantilla invalida no debe guardarse: %v", err)
	}
	if _, _, err := h.uc.CreateTemplate(context.Background(), h.tenant, h.user, CreateTemplateInput{
		Name: "Bienvenida", Kind: domain.KindMarketing, Content: content("<p>x</p>"),
	}); !errors.Is(err, domain.ErrTemplateNameTaken) {
		t.Errorf("nombre repetido: %v", err)
	}
	if _, _, err := h.uc.CreateTemplate(context.Background(), h.tenant, h.user, CreateTemplateInput{
		Name: "otra", Kind: "newsletter", Content: content("<p>x</p>"),
	}); !errors.Is(err, domain.ErrInvalidKind) {
		t.Errorf("kind invalido: %v", err)
	}
	if _, _, err := h.uc.CreateTemplate(context.Background(), h.tenant, uuid.Nil, CreateTemplateInput{
		Name: "otra", Kind: domain.KindMarketing, Content: content("<p>x</p>"),
	}); !errors.Is(err, domain.ErrMissingCreator) {
		t.Errorf("sin usuario: %v", err)
	}
}

func TestPublicarDejaUnaSolaPublicada(t *testing.T) {
	h := newHarness()
	tpl := h.create(t, "pedido")
	ctx := context.Background()

	v2, err := h.uc.CreateVersion(ctx, h.tenant, tpl.ID, h.user, content("<p>v2 {{.name}}</p>"))
	if err != nil || v2.Version != 2 {
		t.Fatalf("CreateVersion: %v %+v", err, v2)
	}

	if _, err := h.uc.PublishVersion(ctx, h.tenant, tpl.ID, 1); err != nil {
		t.Fatalf("publicar v1: %v", err)
	}
	if _, err := h.uc.PublishVersion(ctx, h.tenant, tpl.ID, 2); err != nil {
		t.Fatalf("publicar v2: %v", err)
	}
	if n := h.repo.publishedCount(tpl.ID); n != 1 {
		t.Fatalf("debe quedar una sola publicada, hay %d", n)
	}
	v1, _ := h.repo.GetVersion(ctx, h.tenant, tpl.ID, 1)
	if v1.Status != domain.VersionStatusSuperseded || v1.PublishedAt == nil {
		t.Errorf("la anterior debe quedar supersedida con su fecha: %+v", v1)
	}
	detail, err := h.uc.GetTemplate(ctx, h.tenant, tpl.ID)
	if err != nil || detail.Template.CurrentVersion != 2 || detail.Current == nil || detail.Current.Version != 2 {
		t.Errorf("current_version debe apuntar a la 2: %v %+v", err, detail)
	}
	if len(detail.Versions) != 2 || detail.Versions[0].Version != 2 {
		t.Errorf("el detalle lista las versiones de la mas reciente a la primera: %+v", detail.Versions)
	}
	if len(h.events.published) != 2 || h.events.published[1].Version != 2 || h.events.published[1].TemplateID != tpl.ID {
		t.Errorf("eventos de publicacion inesperados: %+v", h.events.published)
	}

	if _, err := h.uc.PublishVersion(ctx, h.tenant, tpl.ID, 2); !errors.Is(err, domain.ErrVersionAlreadyPublished) {
		t.Errorf("republicar la publicada: %v", err)
	}
	// Volver a la 1 (rollback) es valido y vuelve a dejar una sola publicada.
	if _, err := h.uc.PublishVersion(ctx, h.tenant, tpl.ID, 1); err != nil {
		t.Fatalf("volver a v1: %v", err)
	}
	if n := h.repo.publishedCount(tpl.ID); n != 1 {
		t.Fatalf("tras el rollback debe seguir habiendo una publicada, hay %d", n)
	}
	if _, err := h.uc.PublishVersion(ctx, h.tenant, tpl.ID, 9); !errors.Is(err, domain.ErrVersionNotFound) {
		t.Errorf("version inexistente: %v", err)
	}
}

func TestBorrarExigeArchivada(t *testing.T) {
	h := newHarness()
	tpl := h.create(t, "borrable")
	ctx := context.Background()

	if err := h.uc.DeleteTemplate(ctx, h.tenant, tpl.ID); !errors.Is(err, domain.ErrTemplateNotArchived) {
		t.Fatalf("borrar activa debe fallar con ErrTemplateNotArchived: %v", err)
	}
	archived := domain.TemplateStatusArchived
	if _, err := h.uc.UpdateTemplate(ctx, h.tenant, tpl.ID, UpdateTemplateInput{Status: &archived}); err != nil {
		t.Fatalf("archivar: %v", err)
	}
	if err := h.uc.DeleteTemplate(ctx, h.tenant, tpl.ID); err != nil {
		t.Fatalf("borrar archivada: %v", err)
	}
	if _, err := h.uc.GetTemplate(ctx, h.tenant, tpl.ID); !errors.Is(err, domain.ErrTemplateNotFound) {
		t.Errorf("la plantilla borrada sigue existiendo: %v", err)
	}
	if err := h.uc.DeleteTemplate(ctx, uuid.New(), tpl.ID); !errors.Is(err, domain.ErrTemplateNotFound) {
		t.Errorf("otra empresa no ve la plantilla: %v", err)
	}
}

func TestArchivadaNoAdmiteVersionesNiPublicarNiRenderizar(t *testing.T) {
	h := newHarness()
	tpl := h.create(t, "vieja")
	ctx := context.Background()
	if _, err := h.uc.PublishVersion(ctx, h.tenant, tpl.ID, 1); err != nil {
		t.Fatalf("publicar: %v", err)
	}
	archived := domain.TemplateStatusArchived
	if _, err := h.uc.UpdateTemplate(ctx, h.tenant, tpl.ID, UpdateTemplateInput{Status: &archived}); err != nil {
		t.Fatalf("archivar: %v", err)
	}
	if _, err := h.uc.CreateVersion(ctx, h.tenant, tpl.ID, h.user, content("<p>x</p>")); !errors.Is(err, domain.ErrTemplateArchived) {
		t.Errorf("nueva version sobre archivada: %v", err)
	}
	if _, err := h.uc.PublishVersion(ctx, h.tenant, tpl.ID, 1); !errors.Is(err, domain.ErrTemplateArchived) {
		t.Errorf("publicar sobre archivada: %v", err)
	}
	if _, err := h.uc.Render(ctx, h.tenant, tpl.ID, RenderInput{}); !errors.Is(err, domain.ErrTemplateArchived) {
		t.Errorf("renderizar archivada: %v", err)
	}
	if _, err := h.uc.Render(ctx, h.tenant, tpl.ID, RenderInput{Preview: true}); err != nil {
		t.Errorf("la previsualizacion de una archivada debe funcionar: %v", err)
	}
}

func TestRenderUsaLaPublicadaYPreviewAdmiteBorradores(t *testing.T) {
	h := newHarness()
	tpl := h.create(t, "render")
	ctx := context.Background()
	values := map[string]json.RawMessage{"name": json.RawMessage(`"Ana"`)}

	if _, err := h.uc.Render(ctx, h.tenant, tpl.ID, RenderInput{Values: values}); !errors.Is(err, domain.ErrNoPublishedVersion) {
		t.Fatalf("sin publicada debe fallar con ErrNoPublishedVersion: %v", err)
	}
	one := 1
	if _, err := h.uc.Render(ctx, h.tenant, tpl.ID, RenderInput{Version: &one, Values: values}); !errors.Is(err, domain.ErrVersionNotPublished) {
		t.Fatalf("renderizar un borrador explicito: %v", err)
	}
	out, err := h.uc.Render(ctx, h.tenant, tpl.ID, RenderInput{Version: &one, Values: values, Preview: true})
	if err != nil || out.Version != 1 || out.Subject != "Asunto|Ana" {
		t.Fatalf("preview del borrador: %v %+v", err, out)
	}

	if _, err := h.uc.PublishVersion(ctx, h.tenant, tpl.ID, 1); err != nil {
		t.Fatalf("publicar: %v", err)
	}
	if _, err := h.uc.CreateVersion(ctx, h.tenant, tpl.ID, h.user, content("<p>v2</p>")); err != nil {
		t.Fatalf("v2: %v", err)
	}
	out, err = h.uc.Render(ctx, h.tenant, tpl.ID, RenderInput{Values: values})
	if err != nil || out.Version != 1 {
		t.Fatalf("sin version debe usar la publicada (1): %v %+v", err, out)
	}
	if _, err := h.uc.PublishVersion(ctx, h.tenant, tpl.ID, 2); err != nil {
		t.Fatalf("publicar v2: %v", err)
	}
	// Una version supersedida sigue siendo renderizable por numero: un envio que la fijo
	// no cambia de contenido porque alguien publique otra.
	out, err = h.uc.Render(ctx, h.tenant, tpl.ID, RenderInput{Version: &one, Values: values})
	if err != nil || out.Version != 1 {
		t.Fatalf("la supersedida debe renderizarse por numero: %v %+v", err, out)
	}
	if _, err := h.uc.Render(ctx, h.tenant, tpl.ID, RenderInput{Values: map[string]json.RawMessage{"name": json.RawMessage(`5`)}}); !errors.Is(err, domain.ErrInvalidVariables) {
		t.Errorf("un valor de tipo incorrecto debe fallar con ErrInvalidVariables: %v", err)
	}
}

func TestUpdateTemplate(t *testing.T) {
	h := newHarness()
	tpl := h.create(t, "uno")
	h.create(t, "dos")
	ctx := context.Background()

	if _, err := h.uc.UpdateTemplate(ctx, h.tenant, tpl.ID, UpdateTemplateInput{}); !errors.Is(err, domain.ErrNothingToUpdate) {
		t.Errorf("sin campos: %v", err)
	}
	bad := "deleted"
	if _, err := h.uc.UpdateTemplate(ctx, h.tenant, tpl.ID, UpdateTemplateInput{Status: &bad}); !errors.Is(err, domain.ErrInvalidTemplateStatus) {
		t.Errorf("estado invalido: %v", err)
	}
	taken := "dos"
	if _, err := h.uc.UpdateTemplate(ctx, h.tenant, tpl.ID, UpdateTemplateInput{Name: &taken}); !errors.Is(err, domain.ErrTemplateNameTaken) {
		t.Errorf("renombrar a un nombre en uso: %v", err)
	}
	name, desc := " Uno renombrado ", " descripcion "
	updated, err := h.uc.UpdateTemplate(ctx, h.tenant, tpl.ID, UpdateTemplateInput{Name: &name, Description: &desc})
	if err != nil || updated.Name != "Uno renombrado" || updated.Description != "descripcion" {
		t.Errorf("actualizacion: %v %+v", err, updated)
	}
}

func TestListTemplatesFiltra(t *testing.T) {
	h := newHarness()
	h.create(t, "Bienvenida")
	h.create(t, "Factura")
	ctx := context.Background()
	if _, _, err := h.uc.CreateTemplate(ctx, h.tenant, h.user, CreateTemplateInput{Name: "Promo", Kind: domain.KindMarketing, Content: content("<p>x</p>")}); err != nil {
		t.Fatal(err)
	}
	items, total, err := h.uc.ListTemplates(ctx, h.tenant, ports.ListFilter{Kind: domain.KindMarketing, Limit: 20})
	if err != nil || total != 1 || items[0].Name != "Promo" {
		t.Errorf("filtro por kind: %v %d %+v", err, total, items)
	}
	items, total, err = h.uc.ListTemplates(ctx, h.tenant, ports.ListFilter{Search: "fact", Limit: 20})
	if err != nil || total != 1 || items[0].Name != "Factura" {
		t.Errorf("busqueda: %v %d %+v", err, total, items)
	}
	if _, _, err := h.uc.ListTemplates(ctx, h.tenant, ports.ListFilter{Kind: "x"}); !errors.Is(err, domain.ErrInvalidKind) {
		t.Errorf("kind invalido en el filtro: %v", err)
	}
	if _, _, err := h.uc.ListTemplates(ctx, h.tenant, ports.ListFilter{Status: "x"}); !errors.Is(err, domain.ErrInvalidTemplateStatus) {
		t.Errorf("status invalido en el filtro: %v", err)
	}
}

func TestNormalizePage(t *testing.T) {
	if p, s := NormalizePage(0, 0); p != 1 || s != defaultPageSize {
		t.Errorf("defaults: %d %d", p, s)
	}
	if _, s := NormalizePage(3, 1000); s != maxPageSize {
		t.Errorf("tope: %d", s)
	}
}
