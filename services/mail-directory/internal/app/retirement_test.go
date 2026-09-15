package app

import (
	"context"
	"errors"
	"testing"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
)

// La baja apaga todo lo de la empresa que recibe o autentica, anuncia cada dominio, dominio alias,
// buzon y alias que cambia dentro de la transaccion y no toca a otra empresa. Repetirla no cambia
// ni anuncia nada y conserva la hora de la primera.
func TestLaBajaApagaElDirectorioDeLaEmpresaYLoAnuncia(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	tenant, otra := uuid.New(), uuid.New()
	d := h.addDomain(tenant, "acme.test", domain.DomainLimits{})
	mb := h.addMailbox(tenant, "ana@acme.test", 0)
	h.aliasDomains.items = append(h.aliasDomains.items,
		&domain.AliasDomain{ID: uuid.New(), TenantID: tenant, AliasDomain: "acme-alias.test", TargetDomain: "acme.test", Active: true})
	h.aliases.items = append(h.aliases.items,
		&domain.Alias{ID: uuid.New(), TenantID: tenant, Address: "ventas@acme.test", Goto: mb.Username, Domain: "acme.test", Active: domain.ActiveOn},
		&domain.Alias{ID: uuid.New(), TenantID: tenant, Address: "viejo@acme.test", Goto: mb.Username, Domain: "acme.test", Active: domain.ActiveOff})
	h.retirements.settings = domain.RetirementCounts{AppPasswords: 2, Relayhosts: 1, Transports: 1}
	ajeno := h.addDomain(otra, "otra.test", domain.DomainLimits{})
	ajenoBuzon := h.addMailbox(otra, "eva@otra.test", 0)

	r, err := h.uc.RetireTenant(ctx, tenant)
	if err != nil {
		t.Fatalf("RetireTenant: %v", err)
	}
	want := domain.RetirementCounts{Domains: 1, AliasDomains: 1, Mailboxes: 1, Aliases: 1, AppPasswords: 2, Relayhosts: 1, Transports: 1}
	if r.TenantID != tenant || !r.RetiredAt.Equal(h.retirements.now) || r.Deactivated != want {
		t.Fatalf("baja = %+v; want %+v a las %v", r, want, h.retirements.now)
	}
	if d.Active || mb.Active != domain.ActiveOff || h.aliasDomains.items[0].Active || h.aliases.items[0].Active != domain.ActiveOff {
		t.Fatalf("queda encendido: dominio %v, buzon %d, dominio alias %v, alias %d",
			d.Active, mb.Active, h.aliasDomains.items[0].Active, h.aliases.items[0].Active)
	}
	if !ajeno.Active || ajenoBuzon.Active != domain.ActiveOn {
		t.Fatal("la baja no toca el directorio de otra empresa")
	}
	for subject, n := range map[string]int{"mail.domain.updated": 1, "mail.alias_domain.updated": 1, "mail.mailbox.updated": 1, "mail.alias.updated": 1} {
		if got := h.published(subject); got != n {
			t.Errorf("%s = %d; want %d", subject, got, n)
		}
	}
	if len(h.events.subjects) != 4 || len(h.events.outside) != 0 || h.retirements.exclusive != 1 {
		t.Fatalf("eventos %v (fuera de la transaccion %v), cerrojos exclusivos %d", h.events.subjects, h.events.outside, h.retirements.exclusive)
	}

	again, err := h.uc.RetireTenant(ctx, tenant)
	if err != nil || again.Deactivated != (domain.RetirementCounts{}) || !again.RetiredAt.Equal(r.RetiredAt) {
		t.Fatalf("repetir la baja = %+v, %v; want nada que apagar y la misma hora", again, err)
	}
	if len(h.events.subjects) != 4 {
		t.Fatalf("repetir la baja anuncio %v", h.events.subjects[4:])
	}
}

// Desde la baja el directorio de la empresa no admite escrituras: nada vuelve a recibir ni a
// autenticar y no se anuncia nada. Solo se puede apagar un dominio que ya tiene, que es lo que pide
// domain-service al retirarlo. Otra empresa escribe con normalidad.
func TestUnaEmpresaDadaDeBajaNoEscribeEnSuDirectorio(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	tenant := uuid.New()
	d := h.addDomain(tenant, "acme.test", domain.DomainLimits{})
	mb := h.addMailbox(tenant, "ana@acme.test", 0)
	if _, err := h.uc.RetireTenant(ctx, tenant); err != nil {
		t.Fatalf("RetireTenant: %v", err)
	}
	eventos := len(h.events.subjects)
	on := domain.ActiveOn

	escrituras := map[string]func() error{
		"activar su dominio": func() error { _, err := h.uc.SetDomainActivation(ctx, tenant, "acme.test", true); return err },
		"dar de alta un dominio por la activacion": func() error {
			_, err := h.uc.SetDomainActivation(ctx, tenant, "otro.test", false)
			return err
		},
		"alta de dominio": func() error {
			_, err := h.uc.CreateDomain(ctx, tenant, CreateDomainRequest{Domain: "nuevo.test"})
			return err
		},
		"dominio alias": func() error {
			_, err := h.uc.CreateAliasDomain(ctx, tenant, CreateAliasDomainRequest{AliasDomain: "nuevo-alias.test", TargetDomain: "acme.test"})
			return err
		},
		"alta de buzon": func() error {
			_, err := h.uc.CreateMailbox(ctx, tenant, CreateMailboxRequest{LocalPart: "eva", Domain: "acme.test", Password: "contrasena-de-prueba-1"})
			return err
		},
		"reactivar un buzon": func() error {
			_, err := h.uc.UpdateMailbox(ctx, tenant, mb.ID, UpdateMailboxRequest{Active: &on})
			return err
		},
		"contrasena de un buzon": func() error { return h.uc.SetMailboxPassword(ctx, tenant, mb.ID, "otra-contrasena-larga-1") },
		"contrasena de aplicacion": func() error {
			_, _, err := h.uc.CreateAppPassword(ctx, tenant, mb.ID, CreateAppPasswordRequest{Name: "movil"})
			return err
		},
		"borrar un buzon": func() error { return h.uc.DeleteMailbox(ctx, tenant, mb.ID) },
		"alias": func() error {
			_, err := h.uc.CreateAlias(ctx, tenant, CreateAliasRequest{Address: "ventas@acme.test", Goto: mb.Username})
			return err
		},
		"relayhost": func() error {
			_, err := h.uc.CreateRelayhost(ctx, tenant, CreateRelayhostRequest{Hostname: "smtp.relay.example:587"})
			return err
		},
		"transporte": func() error {
			_, err := h.uc.CreateTransport(ctx, Scope{TenantID: tenant}, CreateTransportRequest{Destination: "partner.example", Nexthop: "[smtp.partner.example]:25"})
			return err
		},
	}
	for nombre, escribir := range escrituras {
		if err := escribir(); !errors.Is(err, domain.ErrTenantRetired) {
			t.Errorf("%s = %v; want ErrTenantRetired", nombre, err)
		}
	}
	if len(h.events.subjects) != eventos || d.Active || mb.Active != domain.ActiveOff || len(h.mailboxes.items) != 1 || len(h.domains.items) != 1 {
		t.Fatalf("una escritura rechazada cambio algo: eventos %v, dominio %v, buzon %d, buzones %d, dominios %d",
			h.events.subjects[eventos:], d.Active, mb.Active, len(h.mailboxes.items), len(h.domains.items))
	}

	if _, err := h.uc.SetDomainActivation(ctx, tenant, "acme.test", false); err != nil {
		t.Fatalf("apagar un dominio que ya tiene: %v", err)
	}
	otra := uuid.New()
	h.addDomain(otra, "otra.test", domain.DomainLimits{})
	if _, err := h.uc.CreateMailbox(ctx, otra, CreateMailboxRequest{LocalPart: "eva", Domain: "otra.test", Password: "contrasena-de-prueba-1"}); err != nil {
		t.Fatalf("otra empresa da de alta un buzon: %v", err)
	}
}

// Una baja que no puede anunciar lo que apaga no queda: la transaccion se deshace entera, sin la
// marca ni el dominio apagado, y el siguiente intento la hace completa.
func TestUnaBajaQueNoPuedeAnunciarseNoQueda(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	tenant := uuid.New()
	h.addDomain(tenant, "acme.test", domain.DomainLimits{})
	h.events.fail = errors.New("outbox caida")
	if _, err := h.uc.RetireTenant(ctx, tenant); err == nil {
		t.Fatal("se esperaba el fallo de la outbox")
	}
	if _, retired := h.retirements.retired[tenant]; retired || !h.domains.items[0].Active {
		t.Fatalf("quedo algo de la baja fallida: marca %v, dominio activo %v", retired, h.domains.items[0].Active)
	}
	if _, err := h.uc.CreateMailbox(ctx, tenant, CreateMailboxRequest{LocalPart: "ana", Domain: "acme.test", Password: "contrasena-de-prueba-1"}); !errors.Is(err, h.events.fail) {
		t.Fatalf("sin baja la empresa sigue escribiendo (aqui falla solo la outbox): %v", err)
	}

	h.events.fail = nil
	r, err := h.uc.RetireTenant(ctx, tenant)
	if err != nil || r.Deactivated.Domains != 1 {
		t.Fatalf("reintento: %+v, %v", r, err)
	}
}
