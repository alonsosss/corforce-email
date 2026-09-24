//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
	"github.com/google/uuid"
)

// Mensajes de SMTP (transactional/08): el origen y la clave viajan con la fila, el MIME se guarda
// aparte y se lee para enviarlo, y la base impide un mensaje de SMTP de marketing o de prueba.
func TestMensajesDeSMTP(t *testing.T) {
	ctx, _, repo := setup(t)
	tenant := uuid.New()
	key := uuid.New()

	msg := newMessage(tenant, domain.StatusQueued, "ana@example.com")
	msg.Origin, msg.APIKeyID = domain.OriginSMTP, &key
	raw := []byte("From: no-reply@shop.example.com\r\nSubject: Hola\r\n\r\nHola\r\n")
	err := repo.Transact(ctx, func(ctx context.Context) error {
		if err := repo.InsertMessage(ctx, msg); err != nil {
			return err
		}
		return repo.InsertRawContent(ctx, tenant, msg.ID, raw)
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetMessage(ctx, tenant, msg.ID)
	if err != nil || got.Origin != domain.OriginSMTP || got.APIKeyID == nil || *got.APIKeyID != key {
		t.Fatalf("origen y clave: %+v %v", got, err)
	}
	content, err := repo.GetRawContent(ctx, tenant, msg.ID)
	if err != nil || string(content) != string(raw) {
		t.Fatalf("MIME: %q %v", content, err)
	}
	if _, err := repo.GetRawContent(ctx, uuid.New(), msg.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("el MIME de otra empresa no se lee: %v", err)
	}

	api := newMessage(tenant, domain.StatusQueued, "eva@example.com")
	if err := repo.InsertMessage(ctx, api); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.GetMessage(ctx, tenant, api.ID); got.Origin != domain.OriginAPI || got.APIKeyID != nil {
		t.Fatalf("sin origen es del API: %+v", got)
	}

	for name, mutate := range map[string]func(*domain.Message){
		"de marketing": func(m *domain.Message) {
			m.Class, m.Unsubscribable, m.CampaignID, m.ContactID = domain.ClassMarketing, true, ptr(uuid.New()), ptr(uuid.New())
		},
		"de prueba": func(m *domain.Message) { m.Test, m.TemplateID, m.TemplateVersion = true, ptr(uuid.New()), ptr(1) },
	} {
		bad := newMessage(tenant, domain.StatusQueued, "luis@example.com")
		bad.Origin = domain.OriginSMTP
		mutate(bad)
		if err := repo.InsertMessage(ctx, bad); err == nil {
			t.Errorf("un mensaje de SMTP %s se rechaza", name)
		}
	}
}
