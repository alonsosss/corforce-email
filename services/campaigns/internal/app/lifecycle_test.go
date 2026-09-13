package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/campaigns/internal/domain"
	"github.com/alonsosss/corforce-email/services/campaigns/internal/ports"
	"github.com/google/uuid"
)

func TestSchedulePinsPublishedVersion(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	c := h.draft("Primavera")
	h.templates.version = 3
	at := h.clock.now().Add(24 * time.Hour)

	got, err := h.uc.Schedule(ctx, h.tenantID, c.ID, at, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.StatusScheduled || got.TemplateVersion == nil || *got.TemplateVersion != 3 || !got.ScheduledAt.Equal(at) {
		t.Fatalf("programada: %+v", got)
	}
	if h.published("campaigns.campaign.scheduled") == nil {
		t.Fatal("falta el evento scheduled")
	}

	h.templates.version = 4
	if v := *h.campaign(c.ID).TemplateVersion; v != 3 {
		t.Fatalf("una publicacion posterior no cambia la campana programada: %d", v)
	}

	explicit := 2
	got, err = h.uc.Schedule(ctx, h.tenantID, c.ID, at.Add(time.Hour), &explicit)
	if err != nil || *got.TemplateVersion != 2 || h.templates.calls != 1 {
		t.Fatalf("reprogramar con version explicita: %+v err=%v llamadas=%d", got, err, h.templates.calls)
	}
}

func TestScheduleValidations(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	c := h.draft("Validaciones")

	if _, err := h.uc.Schedule(ctx, h.tenantID, c.ID, h.clock.now().Add(-time.Hour), nil); !errors.Is(err, domain.ErrScheduleInPast) {
		t.Fatalf("fecha pasada: %v", err)
	}
	if _, err := h.uc.Schedule(ctx, h.tenantID, c.ID, h.clock.now().Add(2*domain.MaxScheduleHorizon), nil); !errors.Is(err, domain.ErrScheduleTooFar) {
		t.Fatalf("fecha lejana: %v", err)
	}
	h.templates.err = domain.ErrTemplateVersionRequired
	if _, err := h.uc.Schedule(ctx, h.tenantID, c.ID, h.clock.now().Add(time.Hour), nil); !errors.Is(err, domain.ErrTemplateVersionRequired) {
		t.Fatalf("version no averiguable: %v", err)
	}
	if h.campaign(c.ID).Status != domain.StatusDraft {
		t.Fatal("un fallo al programar deja la campana en borrador")
	}
	zero := 0
	if _, err := h.uc.Schedule(ctx, h.tenantID, c.ID, h.clock.now().Add(time.Hour), &zero); !errors.Is(err, domain.ErrInvalidCampaign) {
		t.Fatalf("version cero: %v", err)
	}

	s := h.sending("En envio")
	if _, err := h.uc.Schedule(ctx, h.tenantID, s.ID, h.clock.now().Add(time.Hour), nil); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("programar una campana en envio: %v", err)
	}
}

func TestLifecycleTransitions(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	c := h.draft("Ciclo")

	got, err := h.uc.Start(ctx, h.tenantID, c.ID, nil)
	if err != nil || got.Status != domain.StatusSending || got.StartedAt == nil || *got.TemplateVersion != 1 {
		t.Fatalf("start: %+v err=%v", got, err)
	}
	if _, err := h.uc.Start(ctx, h.tenantID, c.ID, nil); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("start dos veces: %v", err)
	}
	if got, err = h.uc.Pause(ctx, h.tenantID, c.ID); err != nil || got.Status != domain.StatusPaused || got.PauseReason != domain.PauseReasonManual {
		t.Fatalf("pause: %+v err=%v", got, err)
	}
	if _, err := h.uc.Pause(ctx, h.tenantID, c.ID); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("pause dos veces: %v", err)
	}
	if got, err = h.uc.Resume(ctx, h.tenantID, c.ID); err != nil || got.Status != domain.StatusSending || got.PauseReason != "" {
		t.Fatalf("resume: %+v err=%v", got, err)
	}
	if err := h.uc.Delete(ctx, h.tenantID, c.ID); !errors.Is(err, domain.ErrNotDeletable) {
		t.Fatalf("borrar una campana en envio: %v", err)
	}
	if got, err = h.uc.Cancel(ctx, h.tenantID, c.ID); err != nil || got.Status != domain.StatusCancelled {
		t.Fatalf("cancel: %+v err=%v", got, err)
	}
	if _, err := h.uc.Resume(ctx, h.tenantID, c.ID); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("reanudar una cancelada: %v", err)
	}
	if _, err := h.uc.Cancel(ctx, h.tenantID, c.ID); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("cancelar dos veces: %v", err)
	}
	for _, subject := range []string{"campaigns.campaign.started", "campaigns.campaign.paused", "campaigns.campaign.resumed", "campaigns.campaign.cancelled"} {
		ev := h.published(subject)
		if ev == nil {
			t.Fatalf("falta el evento %s", subject)
		}
		at, err := time.Parse(time.RFC3339Nano, ev.payload["occurred_at"].(string))
		if err != nil || !at.Equal(h.clock.now()) || ev.payload["campaign_id"] != c.ID.String() || ev.payload["tenant_id"] != h.tenantID.String() {
			t.Fatalf("%s: payload %v", subject, ev.payload)
		}
	}
	if err := h.uc.Delete(ctx, h.tenantID, c.ID); err != nil {
		t.Fatalf("borrar una cancelada: %v", err)
	}
	if _, err := h.uc.Get(ctx, h.tenantID, c.ID); !errors.Is(err, domain.ErrCampaignNotFound) {
		t.Fatalf("tras borrar: %v", err)
	}
	if _, err := h.uc.Get(ctx, uuid.New(), h.draft("Otra").ID); !errors.Is(err, domain.ErrCampaignNotFound) {
		t.Fatalf("otra empresa no ve la campana: %v", err)
	}
}

func TestPausedScheduledResumesToScheduled(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	c := h.draft("Retenida")
	if _, err := h.uc.Schedule(ctx, h.tenantID, c.ID, h.clock.now().Add(2*time.Hour), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := h.uc.Pause(ctx, h.tenantID, c.ID); err != nil {
		t.Fatal(err)
	}
	got, err := h.uc.Resume(ctx, h.tenantID, c.ID)
	if err != nil || got.Status != domain.StatusScheduled || got.StartedAt != nil {
		t.Fatalf("con la fecha en el futuro vuelve a programada: %+v err=%v", got, err)
	}
	if h.published("campaigns.campaign.started") != nil {
		t.Fatal("no arranco: no hay evento started")
	}

	if _, err := h.uc.Pause(ctx, h.tenantID, c.ID); err != nil {
		t.Fatal(err)
	}
	h.clock.advance(3 * time.Hour)
	got, err = h.uc.Resume(ctx, h.tenantID, c.ID)
	if err != nil || got.Status != domain.StatusSending || got.StartedAt == nil {
		t.Fatalf("con la fecha vencida pasa a envio: %+v err=%v", got, err)
	}
	if h.published("campaigns.campaign.started") == nil {
		t.Fatal("falta el evento started")
	}
}

func TestCancelDiscardsPendingPage(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	c := h.sending("Cancelada")
	h.audience.pages[""] = &ports.AudiencePage{Contacts: []domain.Contact{contact("a@example.com")}, NextCursor: strPtr("p2")}
	h.sender.errs = []error{unavailable()}
	if _, err := h.uc.ProcessBatch(ctx, h.tenantID, c.ID); err != nil {
		t.Fatal(err)
	}
	if b := h.batches(c.ID)[0]; !b.PageFetched {
		t.Fatal("el lote debe tener pagina antes de cancelar")
	}
	if _, err := h.uc.Cancel(ctx, h.tenantID, c.ID); err != nil {
		t.Fatal(err)
	}
	if b := h.batches(c.ID)[0]; b.PageFetched || b.Page != nil || b.Recipients != 0 {
		t.Fatalf("la pagina guardada se descarta al cancelar: %+v", b)
	}
	if out, _ := h.uc.ProcessBatch(ctx, h.tenantID, c.ID); out != OutcomeSkipped {
		t.Fatalf("una cancelada no se procesa: %s", out)
	}
}

func TestUpdateRules(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	c := h.draft("Uno")
	h.draft("Dos")

	taken := "DOS"
	if _, err := h.uc.Update(ctx, h.tenantID, c.ID, domain.Patch{Name: &taken}); !errors.Is(err, domain.ErrNameTaken) {
		t.Fatalf("nombre repetido sin distinguir mayusculas: %v", err)
	}
	name := "Tres"
	if got, err := h.uc.Update(ctx, h.tenantID, c.ID, domain.Patch{Name: &name}); err != nil || got.Name != "Tres" {
		t.Fatalf("renombrar: %+v err=%v", got, err)
	}
	if _, err := h.uc.Update(ctx, h.tenantID, c.ID, domain.Patch{}); !errors.Is(err, domain.ErrNothingToUpdate) {
		t.Fatalf("sin cambios: %v", err)
	}
	bad := "a@b"
	if _, err := h.uc.Update(ctx, h.tenantID, c.ID, domain.Patch{FromEmail: &bad}); !errors.Is(err, domain.ErrInvalidCampaign) {
		t.Fatalf("remitente invalido: %v", err)
	}

	paused := h.campaign(c.ID)
	version := 1
	paused.Status, paused.TemplateVersion = domain.StatusPaused, &version
	h.store.campaigns[c.ID] = paused
	audience := domain.Audience{SegmentIDs: []uuid.UUID{uuid.New()}}
	if _, err := h.uc.Update(ctx, h.tenantID, c.ID, domain.Patch{Audience: &audience}); !errors.Is(err, domain.ErrLockedWhilePaused) {
		t.Fatalf("audiencia en pausa: %v", err)
	}
	template := uuid.New()
	if _, err := h.uc.Update(ctx, h.tenantID, c.ID, domain.Patch{TemplateID: &template}); !errors.Is(err, domain.ErrLockedWhilePaused) {
		t.Fatalf("plantilla en pausa: %v", err)
	}
	fromName := "Tienda Norte"
	if got, err := h.uc.Update(ctx, h.tenantID, c.ID, domain.Patch{FromName: &fromName}); err != nil || got.FromName != fromName {
		t.Fatalf("remitente en pausa: %+v err=%v", got, err)
	}

	s := h.sending("En curso")
	if _, err := h.uc.Update(ctx, h.tenantID, s.ID, domain.Patch{FromName: &fromName}); !errors.Is(err, domain.ErrNotEditable) {
		t.Fatalf("editar en envio: %v", err)
	}
}

func TestSendTestUsesTestKeyWithoutContacts(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	c := h.sending("Prueba")

	res, err := h.uc.SendTest(ctx, h.tenantID, c.ID, TestInput{Emails: []string{"qa@example.com", "QA@example.com", "dev@example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.sender.calls) != 1 {
		t.Fatalf("llamadas: %d", len(h.sender.calls))
	}
	call := h.sender.calls[0]
	if !strings.HasPrefix(call.IdempotencyKey, "campaign:"+c.ID.String()+":test:") || call.Tags["test"] != "true" {
		t.Fatalf("clave o etiqueta de prueba: %q %v", call.IdempotencyKey, call.Tags)
	}
	if len(call.Recipients) != 2 || call.TemplateVersion != 1 || h.templates.calls != 0 {
		t.Fatalf("peticion de prueba: %+v llamadas a templates=%d", call, h.templates.calls)
	}
	for _, r := range call.Recipients {
		if r.ContactID == nil || *r.ContactID != domain.TestContactID(c.ID, r.Email) || r.Variables == nil || len(r.Variables) != 0 {
			t.Fatalf("un destinatario de prueba lleva el contacto sintetico y ninguna variable: %+v", r)
		}
	}
	if res.Accepted != 2 {
		t.Fatalf("resultado: %+v", res)
	}
	if got := h.campaign(c.ID).Counters; got != (domain.Counters{}) {
		t.Fatalf("la prueba no toca contadores: %+v", got)
	}
	if len(h.batches(c.ID)) != 0 {
		t.Fatal("la prueba no crea lotes")
	}

	second, _ := h.uc.SendTest(ctx, h.tenantID, c.ID, TestInput{Emails: []string{"qa@example.com"}})
	if second == nil || h.sender.calls[1].IdempotencyKey == call.IdempotencyKey {
		t.Fatal("cada prueba lleva su propia clave")
	}
	tooMany := []string{"a@x.com", "b@x.com", "c@x.com", "d@x.com", "e@x.com", "f@x.com"}
	if _, err := h.uc.SendTest(ctx, h.tenantID, c.ID, TestInput{Emails: tooMany}); !errors.Is(err, domain.ErrInvalidCampaign) {
		t.Fatalf("mas de 5 direcciones: %v", err)
	}
	draft := h.draft("Borrador")
	h.templates.version = 7
	if _, err := h.uc.SendTest(ctx, h.tenantID, draft.ID, TestInput{Emails: []string{"qa@example.com"}}); err != nil {
		t.Fatal(err)
	}
	if last := h.sender.calls[len(h.sender.calls)-1]; last.TemplateVersion != 7 {
		t.Fatalf("un borrador prueba la version publicada: %d", last.TemplateVersion)
	}
}
