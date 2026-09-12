package app

import (
	"context"
	"errors"
	"testing"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
)

func TestCreateAliasNoPisaUnBuzon(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	h.addDomain(tenant, "acme.com", domain.DomainLimits{})
	h.addMailbox(tenant, "ana@acme.com", 0)

	_, err := h.uc.CreateAlias(context.Background(), tenant, CreateAliasRequest{Address: "Ana@acme.com", Goto: "otro@acme.com"})
	if !errors.Is(err, domain.ErrAddressTaken) {
		t.Fatalf("un alias no puede coincidir con un buzon: %v", err)
	}
}

func TestCreateAliasNormalizaYRespetaMaxAliases(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	h.addDomain(tenant, "acme.com", domain.DomainLimits{MaxAliases: 1})

	a, err := h.uc.CreateAlias(context.Background(), tenant, CreateAliasRequest{
		Address: "Ventas@ACME.com", Goto: " Ana@acme.com , externo@Proveedor.net ",
	})
	if err != nil {
		t.Fatalf("alta: %v", err)
	}
	if a.Address != "ventas@acme.com" || a.Goto != "ana@acme.com,externo@proveedor.net" || a.Domain != "acme.com" {
		t.Fatalf("alias normalizado: %+v", a)
	}
	if !a.SenderAllowed || a.Active != domain.ActiveOn {
		t.Fatalf("valores por defecto: %+v", a)
	}
	if h.published("mail.alias.created") != 1 {
		t.Fatalf("evento ausente")
	}

	_, err = h.uc.CreateAlias(context.Background(), tenant, CreateAliasRequest{Address: "@acme.com", Goto: "ana@acme.com"})
	if !errors.Is(err, domain.ErrMaxAliasesReached) {
		t.Fatalf("max_aliases: %v", err)
	}
}

func TestCreateAliasSobreDominioAlias(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	h.addDomain(tenant, "acme.com", domain.DomainLimits{})
	h.aliasDomains.items = append(h.aliasDomains.items, &domain.AliasDomain{TenantID: tenant, AliasDomain: "acme-mail.com", TargetDomain: "acme.com"})

	if _, err := h.uc.CreateAlias(context.Background(), tenant, CreateAliasRequest{Address: "@acme-mail.com", Goto: "ana@acme.com"}); err != nil {
		t.Fatalf("catch-all sobre dominio alias propio: %v", err)
	}
	if _, err := h.uc.CreateAlias(context.Background(), tenant, CreateAliasRequest{Address: "x@ajeno.com", Goto: "ana@acme.com"}); !errors.Is(err, domain.ErrDomainNotOwned) {
		t.Fatalf("alias en dominio ajeno: %v", err)
	}
	if _, err := h.uc.CreateAlias(context.Background(), tenant, CreateAliasRequest{Address: "y@acme.com", Goto: "sin-arroba"}); !errors.Is(err, domain.ErrInvalidGoto) {
		t.Fatalf("goto invalido: %v", err)
	}
}

func TestCreateSpamAliasExigeBuzonPropioYCaducidad(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	h.addDomain(tenant, "acme.com", domain.DomainLimits{})
	h.addMailbox(tenant, "ana@acme.com", 0)

	_, err := h.uc.CreateSpamAlias(context.Background(), tenant, CreateSpamAliasRequest{Address: "tmp1@acme.com", Goto: "nadie@acme.com", Permanent: true})
	if !errors.Is(err, domain.ErrMailboxNotOwned) {
		t.Fatalf("goto que no es buzon: %v", err)
	}
	_, err = h.uc.CreateSpamAlias(context.Background(), tenant, CreateSpamAliasRequest{Address: "tmp1@acme.com", Goto: "ana@acme.com"})
	if !errors.Is(err, domain.ErrValidityRequired) {
		t.Fatalf("sin caducidad ni permanente: %v", err)
	}
	a, err := h.uc.CreateSpamAlias(context.Background(), tenant, CreateSpamAliasRequest{Address: "TMP1@acme.com", Goto: "ana@acme.com", Permanent: true})
	if err != nil || a.Address != "tmp1@acme.com" || a.ValidUntil != nil {
		t.Fatalf("alta valida: %v %+v", err, a)
	}
}

func TestSenderACLYTransportesDePlataforma(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	h.addDomain(tenant, "acme.com", domain.DomainLimits{})
	h.addMailbox(tenant, "ana@acme.com", 0)

	if _, err := h.uc.CreateSenderACL(context.Background(), tenant, SenderACLRequest{LoggedInAs: "ana@acme.com", SendAs: "*"}); !errors.Is(err, domain.ErrWildcardNeedsExtnl) {
		t.Fatalf("comodin sin external: %v", err)
	}
	if _, err := h.uc.CreateSenderACL(context.Background(), tenant, SenderACLRequest{LoggedInAs: "ana@acme.com", SendAs: "x@ajeno.com"}); !errors.Is(err, domain.ErrDomainNotOwned) {
		t.Fatalf("send_as ajeno sin external: %v", err)
	}
	acl, err := h.uc.CreateSenderACL(context.Background(), tenant, SenderACLRequest{LoggedInAs: "Ana@acme.com", SendAs: "@Acme.com"})
	if err != nil || acl.SendAs != "@acme.com" || acl.LoggedInAs != "ana@acme.com" {
		t.Fatalf("acl valida: %v %+v", err, acl)
	}

	tenantScope := Scope{TenantID: tenant}
	if _, err := h.uc.CreateTransport(context.Background(), tenantScope, CreateTransportRequest{Destination: "x.com", Nexthop: "smtp:[relay]:25", Platform: true}); !errors.Is(err, domain.ErrPlatformOnly) {
		t.Fatalf("ruta de plataforma desde una empresa: %v", err)
	}
	platform := Scope{TenantID: tenant, Platform: true}
	tr, err := h.uc.CreateTransport(context.Background(), platform, CreateTransportRequest{Destination: "X.com", Nexthop: "smtp:[relay]:25", Platform: true, Password: "s"})
	if err != nil || tr.TenantID != nil || !tr.HasPassword || tr.Destination != "x.com" {
		t.Fatalf("ruta de plataforma: %v %+v", err, tr)
	}
	if err := h.uc.DeleteTransport(context.Background(), tenantScope, tr.ID); !errors.Is(err, domain.ErrPlatformOnly) {
		t.Fatalf("una empresa no borra rutas de plataforma: %v", err)
	}
}
