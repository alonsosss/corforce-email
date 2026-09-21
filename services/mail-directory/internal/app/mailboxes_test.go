package app

import (
	"context"
	"errors"
	"testing"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/ports"
	"github.com/google/uuid"
)

const testPassword = "una-contrasena-larga-1"

func TestCreateMailboxRespetaMaxMailboxes(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	h.addDomain(tenant, "acme.com", domain.DomainLimits{MaxMailboxes: 1})

	m, err := h.uc.CreateMailbox(context.Background(), tenant, CreateMailboxRequest{
		LocalPart: "Ana", Domain: "ACME.com", Password: testPassword,
	})
	if err != nil {
		t.Fatalf("primer buzon: %v", err)
	}
	if m.Username != "ana@acme.com" || m.PasswordHash != "hash("+testPassword+")" {
		t.Fatalf("buzon normalizado y hasheado: %+v", m)
	}
	if h.published("mail.mailbox.created") != 1 {
		t.Fatalf("evento de alta no publicado: %v", h.events.subjects)
	}

	_, err = h.uc.CreateMailbox(context.Background(), tenant, CreateMailboxRequest{
		LocalPart: "luis", Domain: "acme.com", Password: testPassword,
	})
	if !errors.Is(err, domain.ErrMaxMailboxesReached) {
		t.Fatalf("segundo buzon debia superar max_mailboxes, dio %v", err)
	}
}

func TestCreateMailboxRespetaCuotaDelDominio(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	h.addDomain(tenant, "acme.com", domain.DomainLimits{DefaultQuotaBytes: 100, MaxQuotaBytes: 150, QuotaBytes: 250})
	h.addMailbox(tenant, "ana@acme.com", 150)

	// Toma la cuota por defecto (100): 150 + 100 = 250, justo en el limite del dominio.
	m, err := h.uc.CreateMailbox(context.Background(), tenant, CreateMailboxRequest{
		LocalPart: "luis", Domain: "acme.com", Password: testPassword,
	})
	if err != nil || m.QuotaBytes != 100 {
		t.Fatalf("cuota por defecto: %v (%+v)", err, m)
	}

	quota := int64(1)
	_, err = h.uc.CreateMailbox(context.Background(), tenant, CreateMailboxRequest{
		LocalPart: "eva", Domain: "acme.com", Password: testPassword, QuotaBytes: &quota,
	})
	if !errors.Is(err, domain.ErrDomainQuotaExceeded) {
		t.Fatalf("la suma de cuotas debia superar la del dominio, dio %v", err)
	}

	tooBig := int64(151)
	_, err = h.uc.CreateMailbox(context.Background(), tenant, CreateMailboxRequest{
		LocalPart: "eva", Domain: "acme.com", Password: testPassword, QuotaBytes: &tooBig,
	})
	if !errors.Is(err, domain.ErrQuotaExceedsMax) {
		t.Fatalf("la cuota debia superar max_quota_bytes, dio %v", err)
	}
}

func TestCreateMailboxExigeDominioPropioYContrasena(t *testing.T) {
	h := newHarness()
	tenant, other := uuid.New(), uuid.New()
	h.addDomain(other, "ajeno.com", domain.DomainLimits{})

	_, err := h.uc.CreateMailbox(context.Background(), tenant, CreateMailboxRequest{
		LocalPart: "ana", Domain: "ajeno.com", Password: testPassword,
	})
	if !errors.Is(err, domain.ErrDomainNotOwned) {
		t.Fatalf("dominio de otra empresa: %v", err)
	}
	_, err = h.uc.CreateMailbox(context.Background(), tenant, CreateMailboxRequest{
		LocalPart: "ana", Domain: "ajeno.com", Password: "corta",
	})
	if !errors.Is(err, domain.ErrPasswordTooShort) {
		t.Fatalf("contrasena corta: %v", err)
	}
}

func TestCreateMailboxNoPisaUnAlias(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	h.addDomain(tenant, "acme.com", domain.DomainLimits{})
	h.aliases.items = append(h.aliases.items, &domain.Alias{TenantID: tenant, Address: "ventas@acme.com", Domain: "acme.com"})

	_, err := h.uc.CreateMailbox(context.Background(), tenant, CreateMailboxRequest{
		LocalPart: "ventas", Domain: "acme.com", Password: testPassword,
	})
	if !errors.Is(err, domain.ErrAddressTaken) {
		t.Fatalf("un buzon no puede pisar un alias: %v", err)
	}
}

func TestDeleteMailboxLimpiaLoQueCuelga(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	m := h.addMailbox(tenant, "ana@acme.com", 0)

	if err := h.uc.DeleteMailbox(context.Background(), tenant, m.ID); err != nil {
		t.Fatalf("borrar: %v", err)
	}
	if len(h.mailboxes.quotaDeleted) != 1 || h.mailboxes.quotaDeleted[0] != "ana@acme.com" {
		t.Errorf("uso de cuota no limpiado por username: %v", h.mailboxes.quotaDeleted)
	}
	if h.appPasswords.deleted != 1 || h.sieve.deleted != 1 || h.senderACL.deletedByUser != 1 || h.spamAliases.deletedByGoto != 1 {
		t.Errorf("dependencias no limpiadas: app=%d sieve=%d acl=%d spam=%d",
			h.appPasswords.deleted, h.sieve.deleted, h.senderACL.deletedByUser, h.spamAliases.deletedByGoto)
	}
	if len(h.mailboxes.items) != 0 || h.published("mail.mailbox.deleted") != 1 {
		t.Errorf("buzon no borrado o evento ausente")
	}
	// Un segundo tenant no borra lo que no es suyo.
	if err := h.uc.DeleteMailbox(context.Background(), uuid.New(), m.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("borrado ajeno: %v", err)
	}
}

func TestDesactivarBuzonRevocaContrasenasDeAplicacion(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	m := h.addMailbox(tenant, "ana@acme.com", 0)
	off := domain.ActiveOff

	if _, err := h.uc.UpdateMailbox(context.Background(), tenant, m.ID, UpdateMailboxRequest{Active: &off}); err != nil {
		t.Fatalf("desactivar: %v", err)
	}
	if h.appPasswords.deactivated != 1 {
		t.Fatalf("las contrasenas de aplicacion debian revocarse")
	}
	if _, err := h.uc.UpdateMailbox(context.Background(), tenant, m.ID, UpdateMailboxRequest{}); !errors.Is(err, domain.ErrNothingToUpdate) {
		t.Fatalf("PATCH vacio: %v", err)
	}
}

func TestCreateAppPasswordDevuelveLaClaveUnaVezYGuardaHash(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	m := h.addMailbox(tenant, "ana@acme.com", 0)

	p, plain, err := h.uc.CreateAppPassword(context.Background(), tenant, m.ID, CreateAppPasswordRequest{Name: "movil"})
	if err != nil {
		t.Fatalf("crear: %v", err)
	}
	if len(plain) < 24 || p.PasswordHash != "hash("+plain+")" || p.PasswordHash == plain {
		t.Fatalf("la contrasena debe generarse en el servidor y guardarse hasheada: %q %q", plain, p.PasswordHash)
	}
	if _, _, err := h.uc.CreateAppPassword(context.Background(), tenant, m.ID, CreateAppPasswordRequest{Name: " "}); !errors.Is(err, domain.ErrNameRequired) {
		t.Fatalf("sin nombre: %v", err)
	}
}

// mail-auth compara la contrasena con TODAS las de aplicacion del buzon en cada intento fallido: sin tope,
// quien administra una empresa podria crear miles y hacer que cada intento de entrar cueste el CPU de la celda.
func TestUnBuzonTieneUnMaximoDeContrasenasDeAplicacion(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	ana := h.addMailbox(tenant, "ana@acme.com", 0)
	bea := h.addMailbox(tenant, "bea@acme.com", 0)
	ctx := context.Background()
	var first uuid.UUID
	for i := 0; i < domain.MaxAppPasswordsPerMailbox; i++ {
		p, _, err := h.uc.CreateAppPassword(ctx, tenant, ana.ID, CreateAppPasswordRequest{Name: "cliente"})
		if err != nil {
			t.Fatalf("contrasena %d: %v", i+1, err)
		}
		if i == 0 {
			first = p.ID
		}
	}
	if _, _, err := h.uc.CreateAppPassword(ctx, tenant, ana.ID, CreateAppPasswordRequest{Name: "de mas"}); !errors.Is(err, domain.ErrMaxAppPasswordsReached) {
		t.Fatalf("pasado el maximo: %v", err)
	}
	if _, _, err := h.uc.CreateAppPassword(ctx, tenant, bea.ID, CreateAppPasswordRequest{Name: "cliente"}); err != nil {
		t.Fatalf("el maximo es de cada buzon: %v", err)
	}
	if err := h.uc.DeleteAppPassword(ctx, tenant, ana.ID, first); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.uc.CreateAppPassword(ctx, tenant, ana.ID, CreateAppPasswordRequest{Name: "cliente"}); err != nil {
		t.Fatalf("borrada una vuelve a haber lugar: %v", err)
	}
}

func TestTodaOperacionPasaPorLaTransaccionRLS(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	h.addDomain(tenant, "acme.com", domain.DomainLimits{})
	before := h.tx.calls
	if _, _, err := h.uc.ListMailboxes(context.Background(), tenant, ports.MailboxFilter{}, NormalizedPage()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.uc.GetDomain(context.Background(), tenant, h.domains.items[0].ID); err != nil {
		t.Fatal(err)
	}
	if h.tx.calls != before+2 {
		t.Fatalf("las lecturas tambien deben abrir la transaccion RLS: %d", h.tx.calls-before)
	}
}
