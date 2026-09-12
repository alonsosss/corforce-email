package app

import (
	"context"
	"errors"
	"testing"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/ports"
	"github.com/google/uuid"
)

// NormalizedPage devuelve la primera pagina por defecto; evita repetir NormalizePage en
// los tests.
func NormalizedPage() ports.Page {
	_, _, page := NormalizePage(0, 0)
	return page
}

func TestCreateDomainNaceInactivoYNoRepiteNombres(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	h.domains.foreign = []string{"ajeno.com"}

	d, err := h.uc.CreateDomain(context.Background(), tenant, CreateDomainRequest{Domain: "ACME.com."})
	if err != nil {
		t.Fatalf("alta: %v", err)
	}
	if d.Active || d.Domain != "acme.com" {
		t.Fatalf("el dominio nace inactivo y en minusculas: %+v", d)
	}
	if _, err := h.uc.CreateDomain(context.Background(), tenant, CreateDomainRequest{Domain: "acme.com"}); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("repetido propio: %v", err)
	}
	if _, err := h.uc.CreateDomain(context.Background(), tenant, CreateDomainRequest{Domain: "ajeno.com"}); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("repetido de otra empresa: %v", err)
	}
	if _, err := h.uc.CreateDomain(context.Background(), tenant, CreateDomainRequest{Domain: "no valido"}); !errors.Is(err, domain.ErrInvalidDomainName) {
		t.Fatalf("nombre invalido: %v", err)
	}
	if _, err := h.uc.CreateDomain(context.Background(), tenant, CreateDomainRequest{
		Domain: "otro.com", Limits: domain.DomainLimits{MaxMailboxes: -1},
	}); !errors.Is(err, domain.ErrInvalidLimit) {
		t.Fatalf("limite negativo: %v", err)
	}
}

func TestUpdateDomainNoActivaPorAPI(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	d := h.addDomain(tenant, "acme.com", domain.DomainLimits{})
	on, off := true, false
	if _, err := h.uc.UpdateDomain(context.Background(), tenant, d.ID, UpdateDomainRequest{Active: &on}); !errors.Is(err, domain.ErrActivationNotAllowed) {
		t.Fatalf("activar por API: %v", err)
	}
	if _, err := h.uc.UpdateDomain(context.Background(), tenant, d.ID, UpdateDomainRequest{Active: &off}); err != nil {
		t.Fatalf("apagar por API: %v", err)
	}
	if _, err := h.uc.UpdateDomain(context.Background(), tenant, d.ID, UpdateDomainRequest{RelayhostID: ptrUUID(uuid.New())}); !errors.Is(err, domain.ErrRelayhostNotOwned) {
		t.Fatalf("relayhost ajeno: %v", err)
	}
}

func ptrUUID(id uuid.UUID) *uuid.UUID { return &id }

func TestSetDomainActivationEsIdempotente(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()

	d, err := h.uc.SetDomainActivation(context.Background(), tenant, "Acme.com", true)
	if err != nil {
		t.Fatalf("primera activacion: %v", err)
	}
	if !d.Active || len(h.domains.items) != 1 {
		t.Fatalf("debia crear el dominio activo: %+v", d)
	}
	if h.published("mail.domain.created") != 1 || h.published("mail.domain.activated") != 1 {
		t.Fatalf("eventos de la primera activacion: %v", h.events.subjects)
	}

	again, err := h.uc.SetDomainActivation(context.Background(), tenant, "acme.com", true)
	if err != nil {
		t.Fatalf("segunda activacion: %v", err)
	}
	if again.ID != d.ID || len(h.domains.items) != 1 || h.domains.updated != 0 {
		t.Fatalf("la segunda llamada no debe crear ni escribir: items=%d updated=%d", len(h.domains.items), h.domains.updated)
	}
	if h.published("mail.domain.created") != 1 {
		t.Fatalf("la segunda llamada no debe volver a publicar created: %v", h.events.subjects)
	}

	off, err := h.uc.SetDomainActivation(context.Background(), tenant, "acme.com", false)
	if err != nil || off.Active || h.domains.updated != 1 {
		t.Fatalf("desactivar: %v updated=%d", err, h.domains.updated)
	}

	// Un dominio que ya existe para otra empresa no se puede reclamar.
	if _, err := h.uc.SetDomainActivation(context.Background(), uuid.New(), "acme.com", true); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("activacion sobre dominio ajeno: %v", err)
	}
}

func TestDeleteDomainConDependencias(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	d := h.addDomain(tenant, "acme.com", domain.DomainLimits{})
	busy := &fakeDomainsWithUsage{fakeDomains: h.domains, mailboxes: 1}
	h.uc.domains = busy

	if err := h.uc.DeleteDomain(context.Background(), tenant, d.ID); !errors.Is(err, domain.ErrDomainInUse) {
		t.Fatalf("con buzones debe dar conflicto: %v", err)
	}
	busy.mailboxes = 0
	if err := h.uc.DeleteDomain(context.Background(), tenant, d.ID); err != nil {
		t.Fatalf("sin dependencias: %v", err)
	}
	if h.published("mail.domain.deleted") != 1 {
		t.Fatalf("evento de borrado ausente")
	}
}

type fakeDomainsWithUsage struct {
	*fakeDomains
	mailboxes int64
}

func (f *fakeDomainsWithUsage) Usage(context.Context, uuid.UUID, string) (int64, int64, int64, error) {
	return f.mailboxes, 0, 0, nil
}

func TestCreateAliasDomainExigeTargetPropioYNoPropio(t *testing.T) {
	h := newHarness()
	tenant, other := uuid.New(), uuid.New()
	h.addDomain(tenant, "acme.com", domain.DomainLimits{})
	h.addDomain(tenant, "acme.pe", domain.DomainLimits{})
	h.addDomain(other, "ajeno.com", domain.DomainLimits{})

	_, err := h.uc.CreateAliasDomain(context.Background(), tenant, CreateAliasDomainRequest{AliasDomain: "acme-mail.com", TargetDomain: "ajeno.com"})
	if !errors.Is(err, domain.ErrDomainNotOwned) {
		t.Fatalf("target de otra empresa: %v", err)
	}
	_, err = h.uc.CreateAliasDomain(context.Background(), tenant, CreateAliasDomainRequest{AliasDomain: "acme.pe", TargetDomain: "acme.com"})
	if !errors.Is(err, domain.ErrDomainIsOwnDomain) {
		t.Fatalf("alias que ya es dominio propio: %v", err)
	}
	_, err = h.uc.CreateAliasDomain(context.Background(), tenant, CreateAliasDomainRequest{AliasDomain: "ajeno.com", TargetDomain: "acme.com"})
	if !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("alias que es dominio de otra empresa: %v", err)
	}
	a, err := h.uc.CreateAliasDomain(context.Background(), tenant, CreateAliasDomainRequest{AliasDomain: "Acme-Mail.com", TargetDomain: "acme.com"})
	if err != nil || a.AliasDomain != "acme-mail.com" || !a.Active {
		t.Fatalf("alta valida: %v %+v", err, a)
	}
	if h.published("mail.alias_domain.created") != 1 {
		t.Fatalf("evento ausente")
	}
}
