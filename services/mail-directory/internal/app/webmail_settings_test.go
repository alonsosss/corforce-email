package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
)

func fieldOf(t *testing.T, err error) string {
	t.Helper()
	var fe *domain.FieldError
	if !errors.As(err, &fe) {
		t.Fatalf("se esperaba un FieldError y salio %v", err)
	}
	return fe.Field
}

func TestFirmaDelBuzonPorNombre(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	h.addMailbox(tenant, "ana@acme.test", 0)
	ctx := context.Background()

	s, err := h.uc.SignatureByUsername(ctx, "Ana@Acme.TEST")
	if err != nil || s.Enabled || s.HTML != "" || s.Username != "ana@acme.test" {
		t.Fatalf("sin firma se ve apagada y vacia: %+v %v", s, err)
	}
	saved, err := h.uc.PutSignatureByUsername(ctx, "ana@acme.test", PutSignatureRequest{Enabled: true, HTML: "<p>Ana</p>", Text: "Ana", OnReplies: true})
	if err != nil || saved.TenantID != tenant || saved.Username != "ana@acme.test" {
		t.Fatalf("guardar: %+v %v", saved, err)
	}
	got, _ := h.uc.SignatureByUsername(ctx, "ana@acme.test")
	if !got.Enabled || got.HTML != "<p>Ana</p>" || !got.OnReplies {
		t.Fatalf("releer: %+v", got)
	}
	if _, err := h.uc.PutSignatureByUsername(ctx, "ana@acme.test", PutSignatureRequest{Enabled: true}); fieldOf(t, err) != "html" {
		t.Fatal("una firma activa vacia se rechaza")
	}
	if _, err := h.uc.SignatureByUsername(ctx, "nadie@acme.test"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("buzon inexistente: %v", err)
	}
}

func TestReglasDelBuzonGeneranScriptYNoReenvianAlPropioBuzon(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	h.addMailbox(tenant, "ana@acme.test", 0)
	ctx := context.Background()

	empty, err := h.uc.FiltersByUsername(ctx, "ana@acme.test")
	if err != nil || len(empty.Rules) != 0 || empty.Forwarding.Enabled || empty.Forwarding.Addresses == nil {
		t.Fatalf("sin reglas: %+v %v", empty, err)
	}
	req := PutFiltersRequest{
		Rules: []domain.FilterRule{{Name: "Clientes", Enabled: true,
			Conditions: []domain.FilterCondition{{Field: "from", Op: "contains", Value: "cliente"}},
			Actions:    []domain.FilterAction{{Type: "move", Folder: "Clientes"}}}},
		Forwarding:      domain.Forwarding{Enabled: true, Addresses: []string{"fuera@otro.example"}, KeepCopy: true},
		Reauthenticated: true,
	}
	saved, err := h.uc.PutFiltersByUsername(ctx, "Ana@acme.test", req)
	if err != nil {
		t.Fatal(err)
	}
	if saved.TenantID != tenant || saved.Username != "ana@acme.test" || !strings.Contains(saved.ScriptData, `fileinto :create "Clientes"`) ||
		!strings.Contains(saved.ScriptData, `redirect :copy "fuera@otro.example"`) {
		t.Fatalf("guardado: %+v", saved)
	}
	got, _ := h.uc.FiltersByUsername(ctx, "ana@acme.test")
	if len(got.Rules) != 1 || got.Rules[0].ID == uuid.Nil {
		t.Fatalf("releer: %+v", got)
	}

	self := PutFiltersRequest{Forwarding: domain.Forwarding{Enabled: true, Addresses: []string{"ANA@acme.test"}}}
	if _, err := h.uc.PutFiltersByUsername(ctx, "ana@acme.test", self); fieldOf(t, err) != "forwarding.addresses[0]" {
		t.Fatal("el reenvio al propio buzon se rechaza")
	}
	if h.filters.upserts != 1 {
		t.Fatalf("una entrada invalida no se guarda: %d", h.filters.upserts)
	}
}

func TestCambioDeContrasenaDelPropioBuzonEmiteElEventoEnLaTransaccion(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	m := h.addMailbox(tenant, "ana@acme.test", 0)
	ctx := context.Background()

	if err := h.uc.SetPasswordByUsername(ctx, "ana@acme.test", "corta"); !errors.Is(err, domain.ErrPasswordTooShort) {
		t.Fatalf("misma politica que el administrador: %v", err)
	}
	if err := h.uc.SetPasswordByUsername(ctx, "ana@acme.test", "una-contrasena-bastante-larga"); err != nil {
		t.Fatal(err)
	}
	if h.mailboxes.passwords[m.ID] != "hash(una-contrasena-bastante-larga)" {
		t.Fatalf("hash guardado: %q", h.mailboxes.passwords[m.ID])
	}
	if len(h.events.credentials) != 1 || h.events.credentials[0].credential != domain.CredentialPassword || len(h.events.outside) != 0 {
		t.Fatalf("evento de credenciales dentro de la transaccion: %+v fuera=%v", h.events.credentials, h.events.outside)
	}
	if err := h.uc.SetPasswordByUsername(ctx, "nadie@acme.test", "una-contrasena-bastante-larga"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("buzon inexistente: %v", err)
	}
}

func scheduledReq(username string, at time.Time) CreateScheduledSendRequest {
	return CreateScheduledSendRequest{
		Username: username, MessageID: "<" + uuid.NewString() + "@acme.test>", Folder: "Scheduled", UIDValidity: 1, UID: 9,
		SendAt: at, Subject: "Propuesta", Recipients: []string{"cliente@otro.example"},
	}
}

func TestEnvioProgramadoDelBuzon(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	h.addMailbox(tenant, "ana@acme.test", 0)
	h.addMailbox(uuid.New(), "luis@otra.test", 0)
	ctx := context.Background()

	s, err := h.uc.CreateScheduledSend(ctx, scheduledReq("Ana@acme.test", h.clock.Add(time.Hour)))
	if err != nil || s.TenantID != tenant || s.Username != "ana@acme.test" || s.Status != domain.ScheduledPending {
		t.Fatalf("crear: %+v %v", s, err)
	}
	if _, err := h.uc.CreateScheduledSend(ctx, scheduledReq("ana@acme.test", h.clock.Add(-time.Hour))); fieldOf(t, err) != "send_at" {
		t.Fatal("hora pasada")
	}
	list, err := h.uc.ListScheduledSends(ctx, "ana@acme.test")
	if err != nil || len(list) != 1 || list[0].ID != s.ID {
		t.Fatalf("listar: %+v %v", list, err)
	}
	if other, _ := h.uc.ListScheduledSends(ctx, "luis@otra.test"); len(other) != 0 {
		t.Fatal("otro buzon no ve la fila")
	}
	if _, err := h.uc.RescheduleSend(ctx, "luis@otra.test", s.ID, h.clock.Add(2*time.Hour)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("otro buzon no la reprograma: %v", err)
	}
	moved, err := h.uc.RescheduleSend(ctx, "ana@acme.test", s.ID, h.clock.Add(2*time.Hour))
	if err != nil || !moved.SendAt.Equal(h.clock.Add(2*time.Hour)) {
		t.Fatalf("reprogramar: %+v %v", moved, err)
	}
	if err := h.uc.CancelScheduledSend(ctx, "luis@otra.test", s.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("otro buzon no la cancela: %v", err)
	}
	if err := h.uc.CancelScheduledSend(ctx, "ana@acme.test", s.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.uc.CancelScheduledSend(ctx, "ana@acme.test", s.ID); err != nil {
		t.Fatalf("cancelar dos veces es idempotente: %v", err)
	}
	if _, err := h.uc.RescheduleSend(ctx, "ana@acme.test", s.ID, h.clock.Add(3*time.Hour)); !errors.Is(err, domain.ErrScheduledSendNotPending) {
		t.Fatalf("una cancelada no se reprograma: %v", err)
	}
}

func TestEnvioProgramadoTopePorBuzon(t *testing.T) {
	h := newHarness()
	h.addMailbox(uuid.New(), "ana@acme.test", 0)
	ctx := context.Background()
	for i := 0; i < domain.MaxScheduledPerMailbox; i++ {
		if _, err := h.uc.CreateScheduledSend(ctx, scheduledReq("ana@acme.test", h.clock.Add(time.Hour))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.uc.CreateScheduledSend(ctx, scheduledReq("ana@acme.test", h.clock.Add(time.Hour))); !errors.Is(err, domain.ErrScheduledSendLimit) {
		t.Fatalf("tope: %v", err)
	}
}

func TestReclamarYCerrarConReintentos(t *testing.T) {
	h := newHarness()
	h.addMailbox(uuid.New(), "ana@acme.test", 0)
	ctx := context.Background()
	s, err := h.uc.CreateScheduledSend(ctx, scheduledReq("ana@acme.test", h.clock.Add(time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	if claimed, _ := h.uc.ClaimScheduledSends(ctx, 0, 0); len(claimed) != 0 {
		t.Fatal("una fila futura no se reclama")
	}
	if _, err := h.uc.ClaimScheduledSends(ctx, 1000, 0); fieldOf(t, err) != "limit" {
		t.Fatal("limite fuera de rango")
	}
	for attempt := 1; attempt <= domain.MaxScheduledAttempts; attempt++ {
		h.clock = h.clock.Add(24 * time.Hour)
		claimed, err := h.uc.ClaimScheduledSends(ctx, 10, 60)
		if err != nil || len(claimed) != 1 || claimed[0].Attempts != attempt || claimed[0].Status != domain.ScheduledSending {
			t.Fatalf("intento %d: %+v %v", attempt, claimed, err)
		}
		if err := h.uc.CancelScheduledSend(ctx, "ana@acme.test", s.ID); !errors.Is(err, domain.ErrScheduledSendNotPending) {
			t.Fatalf("una fila en curso no se cancela: %v", err)
		}
		closed, err := h.uc.FinishScheduledSend(ctx, s.ID, domain.ScheduledOutcome{Status: domain.ScheduledFailed, Error: "421", Retry: true})
		if err != nil {
			t.Fatal(err)
		}
		want := domain.ScheduledPending
		if attempt == domain.MaxScheduledAttempts {
			want = domain.ScheduledFailed
		}
		if closed.Status != want {
			t.Fatalf("intento %d cierra en %s", attempt, closed.Status)
		}
	}
	if _, err := h.uc.FinishScheduledSend(ctx, s.ID, domain.ScheduledOutcome{Status: domain.ScheduledSent}); !errors.Is(err, domain.ErrScheduledSendNotClaimed) {
		t.Fatalf("una fila no reclamada no se cierra: %v", err)
	}
	if err := h.uc.CancelScheduledSend(ctx, "ana@acme.test", s.ID); err != nil {
		t.Fatalf("una fallida se retira de la lista: %v", err)
	}
}

func TestBorrarElBuzonBorraSusAjustesDeWebmail(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	m := h.addMailbox(tenant, "ana@acme.test", 0)
	ctx := context.Background()
	if _, err := h.uc.PutSignatureByUsername(ctx, m.Username, PutSignatureRequest{Enabled: true, Text: "Ana"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.uc.CreateScheduledSend(ctx, scheduledReq(m.Username, h.clock.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	if err := h.uc.DeleteMailbox(ctx, tenant, m.ID); err != nil {
		t.Fatal(err)
	}
	if len(h.signatures.items) != 0 || len(h.scheduled.items) != 0 ||
		len(h.filters.deleted) != 1 || h.filters.deleted[0] != m.Username {
		t.Fatalf("quedaron filas: firmas=%d programados=%d reglas=%v", len(h.signatures.items), len(h.scheduled.items), h.filters.deleted)
	}
}
