package app

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/ports"
	"github.com/google/uuid"
)

// Cada escritura del directorio encola su evento DENTRO de su transaccion: billing cuenta
// buzones y dominios con ellos y mail-security mantiene Redis, asi que un evento que
// saliera despues del commit podia perderse y desviar a ambos para siempre.
func TestCadaEscrituraEncolaSuEventoEnLaTransaccion(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	tenant := uuid.New()
	fatal := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}

	d, err := h.uc.SetDomainActivation(ctx, tenant, "acme.com", true)
	fatal("activar", err)
	m, err := h.uc.CreateMailbox(ctx, tenant, CreateMailboxRequest{LocalPart: "ana", Domain: "acme.com", Password: "contrasena-larga-1"})
	fatal("alta de buzon", err)
	name := "Ana"
	_, err = h.uc.UpdateMailbox(ctx, tenant, m.ID, UpdateMailboxRequest{DisplayName: &name})
	fatal("cambio de buzon", err)
	a, err := h.uc.CreateAlias(ctx, tenant, CreateAliasRequest{Address: "ventas@acme.com", Goto: "ana@acme.com"})
	fatal("alta de alias", err)
	target := "ana@acme.com, jefe@proveedor.example"
	_, err = h.uc.UpdateAlias(ctx, tenant, a.ID, UpdateAliasRequest{Goto: &target})
	fatal("cambio de alias", err)
	ad, err := h.uc.CreateAliasDomain(ctx, tenant, CreateAliasDomainRequest{AliasDomain: "acme.net", TargetDomain: "acme.com"})
	fatal("alta de dominio alias", err)
	off := false
	_, err = h.uc.UpdateAliasDomain(ctx, tenant, ad.ID, UpdateAliasDomainRequest{Active: &off})
	fatal("apagar dominio alias", err)
	fatal("baja de dominio alias", h.uc.DeleteAliasDomain(ctx, tenant, ad.ID))
	fatal("baja de alias", h.uc.DeleteAlias(ctx, tenant, a.ID))
	fatal("baja de buzon", h.uc.DeleteMailbox(ctx, tenant, m.ID))
	desc := "principal"
	_, err = h.uc.UpdateDomain(ctx, tenant, d.ID, UpdateDomainRequest{Description: &desc})
	fatal("cambio de dominio", err)
	fatal("baja de dominio", h.uc.DeleteDomain(ctx, tenant, d.ID))

	want := []string{
		"mail.domain.created", "mail.domain.activated", "mail.mailbox.created", "mail.mailbox.updated",
		"mail.alias.created", "mail.alias.updated", "mail.alias_domain.created", "mail.alias_domain.updated",
		"mail.alias_domain.deleted", "mail.alias.deleted", "mail.mailbox.deleted", "mail.domain.updated",
		"mail.domain.deleted",
	}
	if !reflect.DeepEqual(h.events.subjects, want) {
		t.Fatalf("eventos encolados:\n got %v\nwant %v", h.events.subjects, want)
	}
	if len(h.events.outside) != 0 {
		t.Fatalf("eventos emitidos fuera de la transaccion: %v", h.events.outside)
	}
}

// Si la outbox no acepta el evento, la escritura no se confirma: no queda un buzon, una
// baja ni un dominio que nadie vaya a contar.
func TestUnFalloAlEncolarRevierteLaEscritura(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	tenant := uuid.New()
	h.addDomain(tenant, "acme.com", domain.DomainLimits{})
	caida := errors.New("outbox: encolar: permission denied for table event_outbox")

	h.events.fail = caida
	if _, err := h.uc.CreateMailbox(ctx, tenant, CreateMailboxRequest{LocalPart: "ana", Domain: "acme.com", Password: "contrasena-larga-1"}); !errors.Is(err, caida) {
		t.Fatalf("el fallo de la outbox debe llegar al llamante: %v", err)
	}
	if len(h.mailboxes.items) != 0 || len(h.events.subjects) != 0 || h.tx.rolledBack != 1 {
		t.Fatalf("alta revertida: buzones=%d eventos=%v rollbacks=%d", len(h.mailboxes.items), h.events.subjects, h.tx.rolledBack)
	}

	h.events.fail = nil
	m, err := h.uc.CreateMailbox(ctx, tenant, CreateMailboxRequest{LocalPart: "ana", Domain: "acme.com", Password: "contrasena-larga-1"})
	if err != nil {
		t.Fatal(err)
	}
	h.events.fail = caida
	if err := h.uc.DeleteMailbox(ctx, tenant, m.ID); !errors.Is(err, caida) {
		t.Fatalf("baja sin evento: %v", err)
	}
	if len(h.mailboxes.items) != 1 || h.published("mail.mailbox.deleted") != 0 {
		t.Fatalf("la baja debe revertirse entera: buzones=%d", len(h.mailboxes.items))
	}

	if _, err := h.uc.SetDomainActivation(ctx, tenant, "nuevo.com", true); !errors.Is(err, caida) {
		t.Fatalf("activacion sin evento: %v", err)
	}
	if _, err := h.domains.GetByName(ctx, tenant, "nuevo.com"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("el dominio no debe quedar creado: %v", err)
	}
}

func TestListadosNormalizanLaBusqueda(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	tenant := uuid.New()

	if _, _, err := h.uc.ListMailboxes(ctx, tenant, ports.MailboxFilter{Search: "  Ana ", Domain: " ACME.com. "}, NormalizedPage()); err != nil {
		t.Fatal(err)
	}
	if h.mailboxes.lastFilter != (ports.MailboxFilter{Search: "Ana", Domain: "acme.com"}) {
		t.Fatalf("filtro que llega al repositorio: %+v", h.mailboxes.lastFilter)
	}
	if _, _, err := h.uc.ListMailboxes(ctx, tenant, ports.MailboxFilter{Domain: "no es un dominio"}, NormalizedPage()); !errors.Is(err, domain.ErrInvalidDomainName) {
		t.Fatalf("dominio invalido: %v", err)
	}
	if _, _, err := h.uc.ListDomains(ctx, tenant, ports.DomainFilter{Search: strings.Repeat("a", domain.MaxSearchLength+1)}, NormalizedPage()); !errors.Is(err, domain.ErrSearchTooLong) {
		t.Fatalf("busqueda larga: %v", err)
	}
	// El tope cuenta caracteres, no bytes.
	if _, _, err := h.uc.ListDomains(ctx, tenant, ports.DomainFilter{Search: strings.Repeat("ñ", domain.MaxSearchLength)}, NormalizedPage()); err != nil {
		t.Fatalf("busqueda multibyte en el tope: %v", err)
	}
	if h.domains.lastFilter.Search != strings.Repeat("ñ", domain.MaxSearchLength) {
		t.Fatalf("filtro de dominios: %+v", h.domains.lastFilter)
	}
}

// El cambio de la contrasena principal avisa a quien guarda sesiones del buzon (el
// webmail las revoca); si el aviso no se puede encolar, la contrasena no cambia.
func TestCambiarLaContrasenaAvisaEnLaTransaccion(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	tenant := uuid.New()
	h.addDomain(tenant, "acme.com", domain.DomainLimits{})
	m, err := h.uc.CreateMailbox(ctx, tenant, CreateMailboxRequest{LocalPart: "ana", Domain: "acme.com", Password: "contrasena-larga-1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.uc.SetMailboxPassword(ctx, tenant, m.ID, "otra-contrasena-larga-2"); err != nil {
		t.Fatal(err)
	}
	if h.published("mail.mailbox.credentials_changed") != 1 || len(h.events.outside) != 0 {
		t.Fatalf("eventos: %v, fuera de la transaccion: %v", h.events.subjects, h.events.outside)
	}
	if want := (credentialEvent{username: "ana@acme.com", credential: domain.CredentialPassword}); h.events.credentials[0] != want {
		t.Fatalf("aviso %+v; want %+v", h.events.credentials[0], want)
	}

	caida := errors.New("outbox: encolar: permission denied for table event_outbox")
	h.events.fail = caida
	antes := h.tx.rolledBack
	if err := h.uc.SetMailboxPassword(ctx, tenant, m.ID, "tercera-contrasena-3"); !errors.Is(err, caida) {
		t.Fatalf("el fallo de la outbox debe llegar al llamante: %v", err)
	}
	if h.tx.rolledBack != antes+1 || h.published("mail.mailbox.credentials_changed") != 1 {
		t.Fatalf("el cambio debe revertirse: rollbacks=%d eventos=%v", h.tx.rolledBack, h.events.subjects)
	}
}
