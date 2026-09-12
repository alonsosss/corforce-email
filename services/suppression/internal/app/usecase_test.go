package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/suppression/internal/domain"
	"github.com/alonsosss/corforce-email/services/suppression/internal/ports"
	"github.com/google/uuid"
)

func TestAddNormalizaYValidaLaDireccion(t *testing.T) {
	f := newFixture()
	ctx := context.Background()

	e, added, err := f.uc.Add(ctx, f.tenant, AddInput{Email: "  Ana.Perez@Example.COM ", Reason: domain.ReasonManual, Source: "api"})
	if err != nil || !added {
		t.Fatalf("Add: err=%v added=%v", err, added)
	}
	if e.Email != "ana.perez@example.com" {
		t.Fatalf("la direccion no se normalizo: %q", e.Email)
	}

	for _, bad := range []string{"", "sin-arroba", "a@b", "con espacio@example.com", "@example.com"} {
		if _, _, err := f.uc.Add(ctx, f.tenant, AddInput{Email: bad, Reason: domain.ReasonManual}); !errors.Is(err, domain.ErrInvalidEmail) {
			t.Errorf("%q: se esperaba ErrInvalidEmail, hubo %v", bad, err)
		}
	}
	if _, _, err := f.uc.Add(ctx, f.tenant, AddInput{Email: "ok@example.com", Reason: "soft_bounce"}); !errors.Is(err, domain.ErrInvalidReason) {
		t.Fatalf("se esperaba ErrInvalidReason, hubo %v", err)
	}
}

func TestAddRespetaElOrdenDeGravedad(t *testing.T) {
	f := newFixture()
	ctx := context.Background()
	const email = "cliente@example.com"

	exp := f.now.Add(24 * time.Hour)
	if _, err := f.uc.CreateManual(ctx, f.tenant, CreateManualInput{Email: email, Reason: domain.ReasonManual, ExpiresAt: &exp}); err != nil {
		t.Fatal(err)
	}

	// Un rebote duro eleva la exclusion manual temporal a definitiva.
	e, added, err := f.uc.Add(ctx, f.tenant, AddInput{Email: email, Reason: domain.ReasonHardBounce, Source: "ses", Detail: "5.1.1"})
	if err != nil || !added {
		t.Fatalf("hard_bounce sobre manual: err=%v added=%v", err, added)
	}
	if e.Reason != domain.ReasonHardBounce || e.ExpiresAt != nil || e.Source != "ses" {
		t.Fatalf("no se elevo la causa: %+v", e)
	}

	// Una baja no degrada un rebote duro.
	e, added, err = f.uc.Add(ctx, f.tenant, AddInput{Email: email, Reason: domain.ReasonUnsubscribe, Source: "api"})
	if err != nil || added {
		t.Fatalf("unsubscribe sobre hard_bounce: err=%v added=%v", err, added)
	}
	if e.Reason != domain.ReasonHardBounce || e.Source != "ses" {
		t.Fatalf("se degrado la causa: %+v", e)
	}

	// La misma causa es idempotente.
	if _, added, _ = f.uc.Add(ctx, f.tenant, AddInput{Email: email, Reason: domain.ReasonHardBounce, Source: "ses"}); added {
		t.Fatal("repetir la misma causa no debe contar como alta")
	}

	// Una queja si eleva el rebote duro.
	e, added, _ = f.uc.Add(ctx, f.tenant, AddInput{Email: email, Reason: domain.ReasonComplaint, Source: "ses"})
	if !added || e.Reason != domain.ReasonComplaint {
		t.Fatalf("complaint sobre hard_bounce: added=%v reason=%s", added, e.Reason)
	}

	if len(f.entries.entries) != 1 {
		t.Fatalf("debe haber una sola fila por direccion, hay %d", len(f.entries.entries))
	}
	// Tres altas efectivas (manual, hard_bounce, complaint) = tres eventos; las
	// idempotentes no emiten.
	if len(f.events.events) != 3 {
		t.Fatalf("eventos: %v", f.events.events)
	}
}

func TestCheckRespetaExpiresAt(t *testing.T) {
	f := newFixture()
	ctx := context.Background()

	past := f.now.Add(-time.Minute)
	future := f.now.Add(time.Minute)
	f.entries.entries = []*domain.Entry{
		{ID: uuid.New(), TenantID: f.tenant, Email: "caducada@example.com", Reason: domain.ReasonManual, ExpiresAt: &past},
		{ID: uuid.New(), TenantID: f.tenant, Email: "vigente@example.com", Reason: domain.ReasonManual, ExpiresAt: &future},
		{ID: uuid.New(), TenantID: f.tenant, Email: "rebote@example.com", Reason: domain.ReasonHardBounce},
		{ID: uuid.New(), TenantID: uuid.New(), Email: "otra-empresa@example.com", Reason: domain.ReasonComplaint},
	}

	got, err := f.uc.Check(ctx, f.tenant, []string{
		"Caducada@Example.com", "VIGENTE@example.com", "rebote@example.com", "otra-empresa@example.com", "libre@example.com", "no-es-email",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]domain.Reason{"vigente@example.com": domain.ReasonManual, "rebote@example.com": domain.ReasonHardBounce}
	if len(got) != len(want) {
		t.Fatalf("suprimidas: %+v", got)
	}
	for _, s := range got {
		if want[s.Email] != s.Reason {
			t.Errorf("%s: causa %s inesperada", s.Email, s.Reason)
		}
	}

	if _, err := f.uc.Check(ctx, f.tenant, make([]string, MaxCheckEmails+1)); !errors.Is(err, domain.ErrTooManyEmails) {
		t.Fatalf("se esperaba ErrTooManyEmails, hubo %v", err)
	}
}

func TestRemoveRechazaUnaBajaYAuditaElResto(t *testing.T) {
	f := newFixture()
	ctx := context.Background()

	baja := &domain.Entry{ID: uuid.New(), TenantID: f.tenant, Email: "baja@example.com", Reason: domain.ReasonUnsubscribe}
	queja := &domain.Entry{ID: uuid.New(), TenantID: f.tenant, Email: "queja@example.com", Reason: domain.ReasonComplaint}
	f.entries.entries = []*domain.Entry{baja, queja}

	if err := f.uc.Remove(ctx, f.tenant, baja.ID); !errors.Is(err, domain.ErrUnsubscribeProtected) {
		t.Fatalf("borrar una baja: se esperaba ErrUnsubscribeProtected, hubo %v", err)
	}
	if err := f.uc.Remove(ctx, f.tenant, queja.ID); err != nil {
		t.Fatalf("borrar una queja: %v", err)
	}
	if err := f.uc.Remove(ctx, f.tenant, queja.ID); !errors.Is(err, domain.ErrEntryNotFound) {
		t.Fatalf("segundo borrado: se esperaba ErrEntryNotFound, hubo %v", err)
	}
	if len(f.events.events) != 1 || f.events.events[0] != "removed|queja@example.com|complaint" {
		t.Fatalf("eventos: %v", f.events.events)
	}
	// Otra empresa no ve la fila.
	if err := f.uc.Remove(ctx, uuid.New(), baja.ID); !errors.Is(err, domain.ErrEntryNotFound) {
		t.Fatalf("otra empresa: se esperaba ErrEntryNotFound, hubo %v", err)
	}
}

func TestResubscribeSoloLevantaLaBaja(t *testing.T) {
	f := newFixture()
	ctx := context.Background()

	f.entries.entries = []*domain.Entry{
		{ID: uuid.New(), TenantID: f.tenant, Email: "baja@example.com", Reason: domain.ReasonUnsubscribe},
		{ID: uuid.New(), TenantID: f.tenant, Email: "rebote@example.com", Reason: domain.ReasonHardBounce},
	}
	if removed, err := f.uc.Resubscribe(ctx, f.tenant, "Baja@example.com"); err != nil || !removed {
		t.Fatalf("baja: removed=%v err=%v", removed, err)
	}
	if removed, err := f.uc.Resubscribe(ctx, f.tenant, "baja@example.com"); err != nil || removed {
		t.Fatalf("segunda vez debe ser idempotente: removed=%v err=%v", removed, err)
	}
	if removed, err := f.uc.Resubscribe(ctx, f.tenant, "rebote@example.com"); err != nil || removed {
		t.Fatalf("un rebote no se levanta por consentimiento: removed=%v err=%v", removed, err)
	}
	if len(f.entries.entries) != 1 || f.entries.entries[0].Reason != domain.ReasonHardBounce {
		t.Fatalf("estado final: %+v", f.entries.entries)
	}
}

func TestIngestIgnoraRebotesTransitorios(t *testing.T) {
	f := newFixture()
	ctx := context.Background()
	msg := uuid.New()

	res, err := f.uc.Ingest(ctx, DeliveryEvent{Subject: SubjectEmailBounced, TenantID: f.tenant, Email: "lleno@example.com", BounceType: "transient", MessageID: &msg})
	if err != nil || !res.Ignored {
		t.Fatalf("transient: err=%v res=%+v", err, res)
	}
	if len(f.entries.entries) != 0 {
		t.Fatal("un rebote transitorio no debe suprimir")
	}

	res, err = f.uc.Ingest(ctx, DeliveryEvent{Subject: SubjectEmailBounced, TenantID: f.tenant, Email: "Inexistente@example.com", BounceType: "permanent", Detail: "5.1.1", MessageID: &msg})
	if err != nil || !res.Added {
		t.Fatalf("permanent: err=%v res=%+v", err, res)
	}
	e := f.entries.entries[0]
	if e.Reason != domain.ReasonHardBounce || e.Source != SourceSES || e.MessageID == nil || *e.MessageID != msg || e.Email != "inexistente@example.com" {
		t.Fatalf("fila: %+v", e)
	}

	// Reentrega del mismo evento: sin cambios.
	if res, _ = f.uc.Ingest(ctx, DeliveryEvent{Subject: SubjectEmailBounced, TenantID: f.tenant, Email: "inexistente@example.com", BounceType: "permanent", MessageID: &msg}); res.Added || res.Ignored {
		t.Fatalf("reentrega: %+v", res)
	}

	res, err = f.uc.Ingest(ctx, DeliveryEvent{Subject: SubjectEmailComplained, TenantID: f.tenant, Email: "inexistente@example.com"})
	if err != nil || !res.Added || f.entries.entries[0].Reason != domain.ReasonComplaint {
		t.Fatalf("complaint: err=%v res=%+v reason=%s", err, res, f.entries.entries[0].Reason)
	}

	if res, _ = f.uc.Ingest(ctx, DeliveryEvent{Subject: "transactional.email.delivered", TenantID: f.tenant, Email: "x@example.com"}); !res.Ignored {
		t.Fatal("un subject ajeno se ignora")
	}
	if _, err := f.uc.Ingest(ctx, DeliveryEvent{Subject: SubjectEmailComplained, TenantID: f.tenant, Email: "roto"}); !IsInputError(err) {
		t.Fatalf("direccion invalida debe ser error de entrada, hubo %v", err)
	}
}

func TestCreateManualSoloManualYConCaducidadFutura(t *testing.T) {
	f := newFixture()
	ctx := context.Background()

	if _, err := f.uc.CreateManual(ctx, f.tenant, CreateManualInput{Email: "a@example.com", Reason: domain.ReasonHardBounce}); !errors.Is(err, domain.ErrManualOnly) {
		t.Fatalf("se esperaba ErrManualOnly, hubo %v", err)
	}
	past := f.now.Add(-time.Second)
	if _, err := f.uc.CreateManual(ctx, f.tenant, CreateManualInput{Email: "a@example.com", Reason: domain.ReasonManual, ExpiresAt: &past}); !errors.Is(err, domain.ErrExpiryInPast) {
		t.Fatalf("se esperaba ErrExpiryInPast, hubo %v", err)
	}
	if _, err := f.uc.CreateManual(ctx, f.tenant, CreateManualInput{Email: "a@example.com", Reason: domain.ReasonManual}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.uc.CreateManual(ctx, f.tenant, CreateManualInput{Email: "A@example.com", Reason: domain.ReasonManual}); !errors.Is(err, domain.ErrEntryAlreadyExists) {
		t.Fatalf("se esperaba ErrEntryAlreadyExists, hubo %v", err)
	}
}

func TestImportCuentaOmitidasYDejaRastro(t *testing.T) {
	f := newFixture()
	ctx := context.Background()
	user := uuid.New()

	f.entries.entries = []*domain.Entry{{ID: uuid.New(), TenantID: f.tenant, Email: "ya@example.com", Reason: domain.ReasonComplaint}}
	imp, err := f.uc.Import(ctx, f.tenant, ImportInput{
		Emails:    []string{"Nueva@example.com", "nueva@example.com", "ya@example.com", "invalida", "otra@example.com"},
		Reason:    domain.ReasonManual,
		CreatedBy: user,
	})
	if err != nil {
		t.Fatal(err)
	}
	if imp.Total != 5 || imp.Added != 2 || imp.Skipped != 3 {
		t.Fatalf("conteo: %+v", imp)
	}
	if len(f.imports.imports) != 1 || f.imports.imports[0].CreatedBy != user {
		t.Fatalf("rastro: %+v", f.imports.imports)
	}
	if f.entries.find(f.tenant, "ya@example.com").Reason != domain.ReasonComplaint {
		t.Fatal("la carga no debe degradar una queja")
	}
	if len(f.events.events) != 2 {
		t.Fatalf("eventos: %v", f.events.events)
	}

	if _, err := f.uc.Import(ctx, f.tenant, ImportInput{Reason: domain.ReasonManual, CreatedBy: user}); !errors.Is(err, domain.ErrNoEmails) {
		t.Fatalf("se esperaba ErrNoEmails, hubo %v", err)
	}
	if _, err := f.uc.Import(ctx, f.tenant, ImportInput{Emails: make([]string, MaxImportEmails+1), Reason: domain.ReasonManual, CreatedBy: user}); !errors.Is(err, domain.ErrTooManyEmails) {
		t.Fatalf("se esperaba ErrTooManyEmails, hubo %v", err)
	}
}

func TestStatsIncluyeTodasLasCausas(t *testing.T) {
	f := newFixture()
	ctx := context.Background()
	past := f.now.Add(-time.Hour)
	f.entries.entries = []*domain.Entry{
		{ID: uuid.New(), TenantID: f.tenant, Email: "a@example.com", Reason: domain.ReasonComplaint},
		{ID: uuid.New(), TenantID: f.tenant, Email: "b@example.com", Reason: domain.ReasonComplaint},
		{ID: uuid.New(), TenantID: f.tenant, Email: "c@example.com", Reason: domain.ReasonManual, ExpiresAt: &past},
	}
	s, err := f.uc.Stats(ctx, f.tenant)
	if err != nil {
		t.Fatal(err)
	}
	if s.Total != 2 || s.ByReason["complaint"] != 2 || s.ByReason["manual"] != 0 || len(s.ByReason) != len(domain.Reasons()) {
		t.Fatalf("stats: %+v", s)
	}
}

func TestListRechazaCausaDesconocida(t *testing.T) {
	f := newFixture()
	if _, _, err := f.uc.List(context.Background(), f.tenant, ports.ListFilter{Reason: "soft", Page: 1, PerPage: 20}); !errors.Is(err, domain.ErrInvalidReason) {
		t.Fatalf("se esperaba ErrInvalidReason, hubo %v", err)
	}
}
