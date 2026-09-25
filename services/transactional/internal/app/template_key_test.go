package app

import (
	"context"
	"errors"
	"testing"

	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
	"github.com/google/uuid"
)

// fakeKeys resuelve claves de plantilla por empresa.
type fakeKeys struct {
	ids   map[string]uuid.UUID
	calls int
}

func (f *fakeKeys) ResolveTemplateKey(_ context.Context, _ uuid.UUID, key string) (uuid.UUID, error) {
	f.calls++
	id, ok := f.ids[key]
	if !ok {
		return uuid.Nil, domain.ErrTemplateNotFound
	}
	return id, nil
}

func keyCommand(f *fixture, key string) CreateMessagesCommand {
	cmd := templateCommand(f, "ana@example.com")
	cmd.TemplateID = nil
	cmd.TemplateKey = key
	return cmd
}

func TestTemplateKeySeResuelveUnaVezYElMensajeGuardaElID(t *testing.T) {
	f := newFixture(t, Config{})
	f.setDomain(f.tenant, shopDomain, "verified", "both")
	id := uuid.New()
	keys := &fakeKeys{ids: map[string]uuid.UUID{"pedido.confirmado": id}}
	f.deps.TemplateKeys = keys
	f.uc = New(f.deps)

	cmd := keyCommand(f, " pedido.confirmado ")
	cmd.To = append(cmd.To, domain.Recipient{Email: "eva@example.com"})
	res, err := f.uc.CreateMessages(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if keys.calls != 1 || len(f.tpl.calls) != 2 {
		t.Fatalf("una resolucion por peticion y un render por destinatario: %d y %d", keys.calls, len(f.tpl.calls))
	}
	for _, call := range f.tpl.calls {
		if call.TemplateID != id {
			t.Errorf("el render debe usar el id resuelto: %s", call.TemplateID)
		}
	}
	if msg := f.repo.messages[res.Messages[0].ID]; msg.TemplateID == nil || *msg.TemplateID != id {
		t.Errorf("el mensaje guarda el id de la plantilla: %v", msg.TemplateID)
	}
}

func TestTemplateKeyErrores(t *testing.T) {
	f := newFixture(t, Config{})
	f.setDomain(f.tenant, shopDomain, "verified", "both")

	if _, err := f.uc.CreateMessages(ctx, keyCommand(f, "pedido.confirmado")); !errors.Is(err, domain.ErrTemplatesUnavailable) {
		t.Errorf("sin resolutor: %v", err)
	}
	f.deps.TemplateKeys = &fakeKeys{ids: map[string]uuid.UUID{}}
	f.uc = New(f.deps)
	if _, err := f.uc.CreateMessages(ctx, keyCommand(f, "no.existe")); !errors.Is(err, domain.ErrTemplateNotFound) {
		t.Errorf("clave desconocida: %v", err)
	}
	for name, mutate := range map[string]func(*CreateMessagesCommand){
		"id y clave":      func(c *CreateMessagesCommand) { id := uuid.New(); c.TemplateID = &id },
		"clave y cuerpo":  func(c *CreateMessagesCommand) { c.HTML = "<p>x</p>" },
		"clave muy larga": func(c *CreateMessagesCommand) { c.TemplateKey = string(make([]byte, 65)) },
		"clave con copia": func(c *CreateMessagesCommand) { c.Cc = []domain.Recipient{{Email: "eva@example.com"}} },
	} {
		cmd := keyCommand(f, "pedido.confirmado")
		mutate(&cmd)
		var ve *domain.ValidationError
		if _, err := f.uc.CreateMessages(ctx, cmd); !errors.As(err, &ve) {
			t.Errorf("%s: esperaba error de validacion, obtuve %v", name, err)
		}
	}
}
