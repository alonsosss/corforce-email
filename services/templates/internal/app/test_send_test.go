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

type fakeTestSender struct {
	calls []ports.TestSendRequest
	err   error
}

func (f *fakeTestSender) SendTest(_ context.Context, _ uuid.UUID, req ports.TestSendRequest) (*ports.TestSendResult, error) {
	f.calls = append(f.calls, req)
	if f.err != nil {
		return nil, f.err
	}
	out := &ports.TestSendResult{}
	for _, email := range req.To {
		out.Messages = append(out.Messages, ports.TestSendMessage{ID: uuid.New(), Status: "queued", Email: email})
	}
	return out, nil
}

func newTestSendHarness() (*harness, *fakeTestSender) {
	h := newHarness()
	sender := &fakeTestSender{}
	h.uc = New(Deps{Repo: h.repo, Tx: h.tx, Renderer: h.renderer, Events: h.events, TestSender: sender})
	return h, sender
}

func TestSendTestEnviaUnBorradorPorTransactional(t *testing.T) {
	h, sender := newTestSendHarness()
	tpl := h.create(t, "bienvenida")
	values := map[string]json.RawMessage{"name": json.RawMessage(`"Ana"`)}
	out, err := h.uc.SendTest(context.Background(), h.tenant, h.user, tpl.ID, 1, TestSendInput{
		To: []string{" qa@example.com ", "dev@example.com"}, FromEmail: " hola@acme.test ", FromName: " Acme ", Variables: values,
	})
	if err != nil || len(out.Messages) != 2 {
		t.Fatalf("prueba de un borrador: %+v %v", out, err)
	}
	if len(sender.calls) != 1 {
		t.Fatalf("una sola llamada a transactional: %d", len(sender.calls))
	}
	got := sender.calls[0]
	if got.TemplateID != tpl.ID || got.Version != 1 || got.FromEmail != "hola@acme.test" || got.FromName != "Acme" ||
		got.To[0] != "qa@example.com" || got.RequestedBy != h.user || string(got.Variables["name"]) != `"Ana"` {
		t.Fatalf("peticion a transactional: %+v", got)
	}
}

func TestSendTestRechazaAntesDeLlamarATransactional(t *testing.T) {
	six := []string{"a@x.test", "b@x.test", "c@x.test", "d@x.test", "e@x.test", "f@x.test"}
	cases := map[string]struct {
		in      func(tpl uuid.UUID) (uuid.UUID, int, TestSendInput)
		archive bool
		want    error
	}{
		"sin destinatarios": {func(id uuid.UUID) (uuid.UUID, int, TestSendInput) {
			return id, 1, TestSendInput{FromEmail: "hola@acme.test"}
		}, false, domain.ErrInvalidTestSend},
		"mas de cinco": {func(id uuid.UUID) (uuid.UUID, int, TestSendInput) {
			return id, 1, TestSendInput{To: six, FromEmail: "hola@acme.test"}
		}, false, domain.ErrInvalidTestSend},
		"repetido": {func(id uuid.UUID) (uuid.UUID, int, TestSendInput) {
			return id, 1, TestSendInput{To: []string{"qa@x.test", "QA@x.test"}, FromEmail: "hola@acme.test"}
		}, false, domain.ErrInvalidTestSend},
		"remitente con nombre": {func(id uuid.UUID) (uuid.UUID, int, TestSendInput) {
			return id, 1, TestSendInput{To: []string{"qa@x.test"}, FromEmail: "Acme <hola@acme.test>"}
		}, false, domain.ErrInvalidTestSend},
		"respuesta invalida": {func(id uuid.UUID) (uuid.UUID, int, TestSendInput) {
			return id, 1, TestSendInput{To: []string{"qa@x.test"}, FromEmail: "hola@acme.test", ReplyTo: "no"}
		}, false, domain.ErrInvalidTestSend},
		"version inexistente": {func(id uuid.UUID) (uuid.UUID, int, TestSendInput) {
			return id, 9, TestSendInput{To: []string{"qa@x.test"}, FromEmail: "hola@acme.test"}
		}, false, domain.ErrVersionNotFound},
		"plantilla de otra empresa": {func(uuid.UUID) (uuid.UUID, int, TestSendInput) {
			return uuid.New(), 1, TestSendInput{To: []string{"qa@x.test"}, FromEmail: "hola@acme.test"}
		}, false, domain.ErrTemplateNotFound},
		"archivada": {func(id uuid.UUID) (uuid.UUID, int, TestSendInput) {
			return id, 1, TestSendInput{To: []string{"qa@x.test"}, FromEmail: "hola@acme.test"}
		}, true, domain.ErrTemplateArchived},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h, sender := newTestSendHarness()
			tpl := h.create(t, "bienvenida")
			if tc.archive {
				archived := domain.TemplateStatusArchived
				if _, err := h.uc.UpdateTemplate(context.Background(), h.tenant, tpl.ID, UpdateTemplateInput{Status: &archived}); err != nil {
					t.Fatal(err)
				}
			}
			id, version, in := tc.in(tpl.ID)
			if _, err := h.uc.SendTest(context.Background(), h.tenant, h.user, id, version, in); !errors.Is(err, tc.want) {
				t.Fatalf("error %v, se esperaba %v", err, tc.want)
			}
			if len(sender.calls) != 0 {
				t.Fatal("un rechazo local no llama a transactional")
			}
		})
	}
}

func TestSendTestSinTransactionalNoEstaDisponible(t *testing.T) {
	h := newHarness()
	tpl := h.create(t, "bienvenida")
	_, err := h.uc.SendTest(context.Background(), h.tenant, h.user, tpl.ID, 1, TestSendInput{To: []string{"qa@x.test"}, FromEmail: "hola@acme.test"})
	if !errors.Is(err, domain.ErrTestSendUnavailable) {
		t.Fatalf("sin TRANSACTIONAL_URL: %v", err)
	}
}

// El render de prueba admite el borrador y completa con valores de ejemplo lo que no llega,
// tambien una variable requerida; no admite una plantilla archivada ni omitir la version.
func TestRenderDePruebaAdmiteBorradorYCompletaValores(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	tpl, _, err := h.uc.CreateTemplate(ctx, h.tenant, h.user, CreateTemplateInput{
		Name: "pedido", Kind: domain.KindTransactional,
		Content: domain.Content{Subject: "Asunto", HTML: "<p>{{.name}}</p>", Variables: []domain.Variable{{Name: "name", Type: domain.VarString, Required: true}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	one := 1
	if _, err := h.uc.Render(ctx, h.tenant, tpl.ID, RenderInput{Version: &one}); !errors.Is(err, domain.ErrVersionNotPublished) {
		t.Fatalf("un envio real no renderiza un borrador: %v", err)
	}
	out, err := h.uc.Render(ctx, h.tenant, tpl.ID, RenderInput{Version: &one, Test: true})
	if err != nil || out.HTML != "<p>"+sampleString+"</p>" {
		t.Fatalf("render de prueba con valor de ejemplo: %+v %v", out, err)
	}
	out, err = h.uc.Render(ctx, h.tenant, tpl.ID, RenderInput{Version: &one, Test: true, Values: map[string]json.RawMessage{"name": json.RawMessage(`"Ana"`)}})
	if err != nil || out.HTML != "<p>Ana</p>" || out.Kind != domain.KindTransactional {
		t.Fatalf("render de prueba con el valor dado: %+v %v", out, err)
	}
	if _, err := h.uc.Render(ctx, h.tenant, tpl.ID, RenderInput{Test: true}); !errors.Is(err, domain.ErrInvalidTestSend) {
		t.Fatalf("sin version: %v", err)
	}
	archived := domain.TemplateStatusArchived
	if _, err := h.uc.UpdateTemplate(ctx, h.tenant, tpl.ID, UpdateTemplateInput{Status: &archived}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.uc.Render(ctx, h.tenant, tpl.ID, RenderInput{Version: &one, Test: true}); !errors.Is(err, domain.ErrTemplateArchived) {
		t.Fatalf("archivada: %v", err)
	}
}

// El alta con el primer diseno del editor guarda la version 1 con su documento, sin paso
// intermedio: es lo que usa "Empezar desde la galeria" y "En blanco".
func TestCreateTemplateConElDisenoDelEditor(t *testing.T) {
	h := newHarness()
	editor := &domain.EditorDocument{Kind: domain.EditorKindGrapesJSMJML, Project: json.RawMessage(`{"pages":[]}`), MJML: "<mjml><mj-body></mj-body></mjml>"}
	_, v, err := h.uc.CreateTemplate(context.Background(), h.tenant, h.user, CreateTemplateInput{
		Name: "desde la galeria", Kind: domain.KindMarketing,
		Content: domain.Content{Subject: "Hola", HTML: "<p>hola</p>", Editor: editor},
	})
	if err != nil {
		t.Fatal(err)
	}
	if v.Version != 1 || v.Editor == nil || v.Editor.Kind != domain.EditorKindGrapesJSMJML || v.Editor.MJML != editor.MJML {
		t.Fatalf("la version 1 guarda el diseno: %+v", v.Editor)
	}
}
