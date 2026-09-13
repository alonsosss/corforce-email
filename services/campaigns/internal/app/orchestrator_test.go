package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/campaigns/internal/domain"
	"github.com/alonsosss/corforce-email/services/campaigns/internal/ports"
)

func unavailable() error { return fmt.Errorf("%w: status 503", ports.ErrUnavailable) }

func TestProcessBatchNewBatchThenRetryWithSameKey(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	c := h.sending("Otono")
	h.audience.pages[""] = &ports.AudiencePage{
		Contacts:   []domain.Contact{contact("a@example.com"), contact("b@example.com")},
		NextCursor: strPtr("p2"),
	}
	h.audience.pages["p2"] = &ports.AudiencePage{Contacts: []domain.Contact{contact("c@example.com")}}
	h.sender.errs = []error{unavailable()}

	out, err := h.uc.ProcessBatch(ctx, h.tenantID, c.ID)
	if err != nil || out != OutcomeRetrying {
		t.Fatalf("primer intento: outcome=%s err=%v", out, err)
	}
	bs := h.batches(c.ID)
	if len(bs) != 1 {
		t.Fatalf("se esperaba un lote, hay %d", len(bs))
	}
	b := bs[0]
	if b.Seq != 1 || b.Status != domain.BatchPending || b.Attempts != 1 || !b.PageFetched || b.Recipients != 2 ||
		b.CursorIn != nil || b.CursorOut == nil || *b.CursorOut != "p2" || b.LeaseToken != nil {
		t.Fatalf("lote tras el fallo: %+v", b)
	}
	if ra := h.campaign(c.ID).ResumeAfter; ra == nil || !ra.Equal(h.clock.now().Add(domain.RetryBackoff(1))) {
		t.Fatalf("resume_after tras el fallo: %v", ra)
	}

	if out, _ := h.uc.ProcessBatch(ctx, h.tenantID, c.ID); out != OutcomeSkipped {
		t.Fatalf("antes de la espera no debe reintentar: %s", out)
	}

	h.clock.advance(domain.RetryBackoff(1))
	out, err = h.uc.ProcessBatch(ctx, h.tenantID, c.ID)
	if err != nil || out != OutcomeDelivered {
		t.Fatalf("reintento: outcome=%s err=%v", out, err)
	}
	if len(h.sender.calls) != 2 {
		t.Fatalf("llamadas a transactional: %d", len(h.sender.calls))
	}
	wantKey := domain.BatchIdempotencyKey(c.ID, 1)
	for i, call := range h.sender.calls {
		if call.IdempotencyKey != wantKey {
			t.Fatalf("llamada %d con clave %q, se esperaba %q", i, call.IdempotencyKey, wantKey)
		}
	}
	if len(h.audience.calls) != 1 {
		t.Fatalf("el reintento no debe volver a pedir la pagina: %d consultas", len(h.audience.calls))
	}
	camp := h.campaign(c.ID)
	if camp.Counters.Targeted != 2 || camp.Counters.Accepted != 2 || camp.ResumeAfter != nil {
		t.Fatalf("contadores tras el lote 1: %+v resume_after=%v", camp.Counters, camp.ResumeAfter)
	}

	out, err = h.uc.ProcessBatch(ctx, h.tenantID, c.ID)
	if err != nil || out != OutcomeCompleted {
		t.Fatalf("ultimo lote: outcome=%s err=%v", out, err)
	}
	if q := h.audience.calls[1]; q.Cursor == nil || *q.Cursor != "p2" || q.Limit != domain.MaxBatchSize {
		t.Fatalf("el lote 2 debe pedir la pagina con el cursor del 1: %+v", q)
	}
	if key := h.sender.calls[2].IdempotencyKey; key != domain.BatchIdempotencyKey(c.ID, 2) {
		t.Fatalf("clave del lote 2: %q", key)
	}
	camp = h.campaign(c.ID)
	if camp.Status != domain.StatusCompleted || camp.CompletedAt == nil || camp.Counters.Targeted != 3 || camp.Counters.Accepted != 3 {
		t.Fatalf("campana al terminar: status=%s counters=%+v", camp.Status, camp.Counters)
	}
	if h.published("campaigns.campaign.completed") == nil {
		t.Fatal("falta el evento completed")
	}

	first := h.sender.calls[0]
	r := first.Recipients[0]
	if r.ContactID == nil || string(r.Variables["first_name"]) != `"Ana"` || string(r.Variables["plan"]) != `"pro"` ||
		string(r.Variables["email"]) != `"a@example.com"` || r.Name != "Ana Diaz" {
		t.Fatalf("destinatario mal armado: %+v vars=%v", r, r.Variables)
	}
	if first.TemplateVersion != 1 || first.FromEmail != c.FromEmail || first.CampaignID != c.ID {
		t.Fatalf("peticion mal armada: %+v", first)
	}
}

func TestProcessBatchCrashAfterAcceptDoesNotDuplicate(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	c := h.sending("Invierno")
	h.audience.pages[""] = &ports.AudiencePage{Contacts: []domain.Contact{contact("a@example.com"), contact("b@example.com")}}
	h.store.failMarkDelivered = 1

	out, err := h.uc.ProcessBatch(ctx, h.tenantID, c.ID)
	if !errors.Is(err, errCrash) || out != OutcomeAborted {
		t.Fatalf("caida: outcome=%s err=%v", out, err)
	}
	if h.sender.created != 2 {
		t.Fatalf("transactional debio aceptar 2, creo %d", h.sender.created)
	}
	b := h.batches(c.ID)[0]
	if b.Status != domain.BatchPending || b.LeaseToken == nil || !b.PageFetched {
		t.Fatalf("tras la caida el lote debe seguir pendiente y reservado: %+v", b)
	}
	if got := h.campaign(c.ID).Counters; got.Accepted != 0 || got.Targeted != 0 {
		t.Fatalf("nada debio contarse todavia: %+v", got)
	}

	if out, _ := h.uc.ProcessBatch(ctx, h.tenantID, c.ID); out != OutcomeSkipped {
		t.Fatalf("con la reserva viva nadie mas debe tomar el lote: %s", out)
	}

	h.clock.advance(domain.BatchLease)
	out, err = h.uc.ProcessBatch(ctx, h.tenantID, c.ID)
	if err != nil || out != OutcomeCompleted {
		t.Fatalf("reanudacion: outcome=%s err=%v", out, err)
	}
	if len(h.sender.calls) != 2 || h.sender.calls[0].IdempotencyKey != h.sender.calls[1].IdempotencyKey {
		t.Fatalf("el reintento debe usar la misma clave: %+v", h.sender.calls)
	}
	if h.sender.created != 2 {
		t.Fatalf("duplicados: transactional creo %d mensajes", h.sender.created)
	}
	if got := h.campaign(c.ID).Counters; got.Accepted != 2 || got.Targeted != 2 {
		t.Fatalf("el lote debe contarse una vez: %+v", got)
	}
}

func TestProcessBatchEmptyAudienceCompletes(t *testing.T) {
	h := newHarness()
	c := h.sending("Vacia")
	out, err := h.uc.ProcessBatch(context.Background(), h.tenantID, c.ID)
	if err != nil || out != OutcomeCompleted {
		t.Fatalf("outcome=%s err=%v", out, err)
	}
	if len(h.sender.calls) != 0 {
		t.Fatal("una pagina vacia no se envia")
	}
	if camp := h.campaign(c.ID); camp.Status != domain.StatusCompleted || camp.Counters.Targeted != 0 {
		t.Fatalf("campana: %+v", camp)
	}
}

func TestProcessBatchSuppressedCounted(t *testing.T) {
	h := newHarness()
	c := h.sending("Supresion")
	h.audience.pages[""] = &ports.AudiencePage{Contacts: []domain.Contact{contact("a@example.com"), contact("baja@example.com")}}
	h.sender.suppress["baja@example.com"] = true
	if out, err := h.uc.ProcessBatch(context.Background(), h.tenantID, c.ID); err != nil || out != OutcomeCompleted {
		t.Fatalf("outcome=%s err=%v", out, err)
	}
	got := h.campaign(c.ID).Counters
	if got.Targeted != 2 || got.Accepted != 1 || got.Suppressed != 1 {
		t.Fatalf("contadores: %+v", got)
	}
	if b := h.batches(c.ID)[0]; b.Accepted != 1 || b.Suppressed != 1 || b.Page != nil {
		t.Fatalf("lote: %+v", b)
	}
}

func TestProcessBatchRateLimitedDoesNotAdvanceCursor(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	c := h.sending("Tasa")
	h.audience.pages[""] = &ports.AudiencePage{Contacts: []domain.Contact{contact("a@example.com")}, NextCursor: strPtr("p2")}
	h.sender.errs = []error{&ports.RateLimitedError{RetryAfter: 2 * time.Minute}}

	out, err := h.uc.ProcessBatch(ctx, h.tenantID, c.ID)
	if err != nil || out != OutcomeRateLimited {
		t.Fatalf("outcome=%s err=%v", out, err)
	}
	if ra := h.campaign(c.ID).ResumeAfter; ra == nil || !ra.Equal(h.clock.now().Add(2*time.Minute)) {
		t.Fatalf("resume_after: %v", ra)
	}
	bs := h.batches(c.ID)
	if len(bs) != 1 || bs[0].Status != domain.BatchPending || bs[0].Attempts != 0 || bs[0].LeaseToken != nil {
		t.Fatalf("el 429 no cuenta intento ni crea lote nuevo: %+v", bs)
	}
	if out, _ := h.uc.ProcessBatch(ctx, h.tenantID, c.ID); out != OutcomeSkipped {
		t.Fatalf("antes de Retry-After no se envia: %s", out)
	}

	h.clock.advance(2 * time.Minute)
	if out, err := h.uc.ProcessBatch(ctx, h.tenantID, c.ID); err != nil || out != OutcomeDelivered {
		t.Fatalf("tras la espera: outcome=%s err=%v", out, err)
	}
	if h.sender.calls[1].IdempotencyKey != domain.BatchIdempotencyKey(c.ID, 1) || len(h.audience.calls) != 1 {
		t.Fatalf("se reintenta el mismo lote con la misma pagina: %+v", h.sender.calls[1])
	}
	if len(h.batches(c.ID)) != 1 {
		t.Fatal("el lote 2 no existe hasta que el 1 se entrega")
	}
	if _, err := h.uc.ProcessBatch(ctx, h.tenantID, c.ID); err != nil {
		t.Fatal(err)
	}
	if b2 := h.batches(c.ID)[1]; b2.Seq != 2 || b2.CursorIn == nil || *b2.CursorIn != "p2" {
		t.Fatalf("lote 2: %+v", b2)
	}
}

func TestProcessBatchBlockedPauses(t *testing.T) {
	for _, code := range []string{"SENDING_RESTRICTED", "PLAN_LIMIT_REACHED"} {
		t.Run(code, func(t *testing.T) {
			h := newHarness()
			c := h.sending("Bloqueo " + code)
			h.audience.pages[""] = &ports.AudiencePage{Contacts: []domain.Contact{contact("a@example.com")}}
			h.sender.errs = []error{&ports.BlockedError{Code: code, Message: "sin cupo"}}

			out, err := h.uc.ProcessBatch(context.Background(), h.tenantID, c.ID)
			if err != nil || out != OutcomePaused {
				t.Fatalf("outcome=%s err=%v", out, err)
			}
			camp := h.campaign(c.ID)
			if camp.Status != domain.StatusPaused || !strings.Contains(camp.PauseReason, code) {
				t.Fatalf("campana: status=%s reason=%q", camp.Status, camp.PauseReason)
			}
			if b := h.batches(c.ID)[0]; b.Status != domain.BatchPending || b.Attempts != 0 || b.LeaseToken != nil {
				t.Fatalf("lote: %+v", b)
			}
			ev := h.published("campaigns.campaign.paused")
			if ev == nil || !strings.Contains(ev.payload["reason"].(string), code) {
				t.Fatalf("evento paused: %+v", ev)
			}
		})
	}
}

func TestProcessBatchRejectedFails(t *testing.T) {
	h := newHarness()
	c := h.sending("Rechazo")
	h.audience.pages[""] = &ports.AudiencePage{Contacts: []domain.Contact{contact("a@example.com")}}
	h.sender.errs = []error{&ports.RejectedError{Message: "dominio de envio no verificado"}}

	out, err := h.uc.ProcessBatch(context.Background(), h.tenantID, c.ID)
	if err != nil || out != OutcomeFailed {
		t.Fatalf("outcome=%s err=%v", out, err)
	}
	camp := h.campaign(c.ID)
	if camp.Status != domain.StatusFailed || !strings.Contains(camp.FailureReason, "no verificado") {
		t.Fatalf("campana: status=%s reason=%q", camp.Status, camp.FailureReason)
	}
	if b := h.batches(c.ID)[0]; b.Status != domain.BatchFailed || b.Page != nil {
		t.Fatalf("el lote rechazado queda fallido y sin pagina: %+v", b)
	}
	if h.published("campaigns.campaign.failed") == nil {
		t.Fatal("falta el evento failed")
	}
}

// TestProcessBatchTemplateRejectionsFailWithCode: los 422 de plantilla de transactional
// cierran la campana como fallida con el codigo al principio del motivo.
func TestProcessBatchTemplateRejectionsFailWithCode(t *testing.T) {
	for _, code := range []string{"TEMPLATE_MISSING_UNSUBSCRIBE", "TEMPLATE_NOT_MARKETING"} {
		t.Run(code, func(t *testing.T) {
			h := newHarness()
			c := h.sending("Plantilla " + code)
			h.audience.pages[""] = &ports.AudiencePage{Contacts: []domain.Contact{contact("a@example.com")}, NextCursor: strPtr("p2")}
			h.sender.errs = []error{&ports.RejectedError{Code: code, Message: "la plantilla no sirve para campanas"}}

			out, err := h.uc.ProcessBatch(context.Background(), h.tenantID, c.ID)
			if err != nil || out != OutcomeFailed {
				t.Fatalf("outcome=%s err=%v", out, err)
			}
			camp := h.campaign(c.ID)
			if camp.Status != domain.StatusFailed || !strings.HasPrefix(camp.FailureReason, code+": ") {
				t.Fatalf("campana: status=%s reason=%q", camp.Status, camp.FailureReason)
			}
			b := h.batches(c.ID)[0]
			if b.Status != domain.BatchFailed || !strings.Contains(b.LastError, code) || b.Page != nil {
				t.Fatalf("lote: %+v", b)
			}
			ev := h.published("campaigns.campaign.failed")
			if ev == nil || !strings.HasPrefix(ev.payload["reason"].(string), code) {
				t.Fatalf("evento failed: %+v", ev)
			}
			if out, _ := h.uc.ProcessBatch(context.Background(), h.tenantID, c.ID); out != OutcomeSkipped || len(h.batches(c.ID)) != 1 {
				t.Fatal("una campana fallida no crea mas lotes")
			}
		})
	}
}

func TestProcessBatchTemplatesUnavailableRetries(t *testing.T) {
	h := newHarness()
	c := h.sending("Templates caido")
	h.audience.pages[""] = &ports.AudiencePage{Contacts: []domain.Contact{contact("a@example.com")}}
	h.sender.errs = []error{fmt.Errorf("%w: status 503 TEMPLATES_UNAVAILABLE", ports.ErrUnavailable)}
	if out, err := h.uc.ProcessBatch(context.Background(), h.tenantID, c.ID); err != nil || out != OutcomeRetrying {
		t.Fatalf("503 de plantillas se reintenta: outcome=%s err=%v", out, err)
	}
	if camp := h.campaign(c.ID); camp.Status != domain.StatusSending {
		t.Fatalf("la campana sigue en envio: %s", camp.Status)
	}
}

func TestProcessBatchAudienceRejectedFails(t *testing.T) {
	h := newHarness()
	c := h.sending("Audiencia")
	h.audience.errs = []error{&ports.RejectedError{Message: "segmento inexistente"}}
	out, err := h.uc.ProcessBatch(context.Background(), h.tenantID, c.ID)
	if err != nil || out != OutcomeFailed {
		t.Fatalf("outcome=%s err=%v", out, err)
	}
	if len(h.sender.calls) != 0 {
		t.Fatal("sin pagina no se llama a transactional")
	}
}

func TestProcessBatchPausesAfterMaxAttempts(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	c := h.sending("Caida")
	h.audience.pages[""] = &ports.AudiencePage{Contacts: []domain.Contact{contact("a@example.com")}}
	for i := 0; i < domain.MaxBatchAttempts; i++ {
		h.sender.errs = append(h.sender.errs, unavailable())
	}

	for i := 1; i <= domain.MaxBatchAttempts; i++ {
		out, err := h.uc.ProcessBatch(ctx, h.tenantID, c.ID)
		if err != nil {
			t.Fatalf("intento %d: %v", i, err)
		}
		if i < domain.MaxBatchAttempts {
			if out != OutcomeRetrying {
				t.Fatalf("intento %d: outcome=%s", i, out)
			}
			h.clock.advance(domain.RetryBackoff(i))
			continue
		}
		if out != OutcomePaused {
			t.Fatalf("intento %d: se esperaba la pausa, hubo %s", i, out)
		}
	}
	camp := h.campaign(c.ID)
	if camp.Status != domain.StatusPaused || !strings.Contains(camp.PauseReason, "10 intentos") {
		t.Fatalf("campana: status=%s reason=%q", camp.Status, camp.PauseReason)
	}
	if b := h.batches(c.ID)[0]; b.Attempts != domain.MaxBatchAttempts {
		t.Fatalf("intentos: %d", b.Attempts)
	}

	if _, err := h.uc.Resume(ctx, h.tenantID, c.ID); err != nil {
		t.Fatal(err)
	}
	if b := h.batches(c.ID)[0]; b.Attempts != 0 {
		t.Fatalf("reanudar debe poner a cero los intentos: %d", b.Attempts)
	}
	out, err := h.uc.ProcessBatch(ctx, h.tenantID, c.ID)
	if err != nil || out != OutcomeCompleted {
		t.Fatalf("tras reanudar: outcome=%s err=%v", out, err)
	}
	wantKey := domain.BatchIdempotencyKey(c.ID, 1)
	for _, call := range h.sender.calls {
		if call.IdempotencyKey != wantKey {
			t.Fatalf("todos los intentos van con la misma clave: %q", call.IdempotencyKey)
		}
	}
	if h.sender.created != 1 {
		t.Fatalf("mensajes creados: %d", h.sender.created)
	}
}

func TestProcessBatchSkipsLockedOrLeased(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	c := h.sending("Ocupada")

	h.store.lockedByOther[c.ID] = true
	if out, err := h.uc.ProcessBatch(ctx, h.tenantID, c.ID); err != nil || out != OutcomeSkipped {
		t.Fatalf("campana bloqueada por otro: outcome=%s err=%v", out, err)
	}
	if len(h.audience.calls) != 0 || len(h.batches(c.ID)) != 0 {
		t.Fatal("sin la campana no se reclama ni se consulta nada")
	}

	delete(h.store.lockedByOther, c.ID)
	camp := h.campaign(c.ID)
	b := domain.NextBatch(&camp, nil, h.clock.now())
	h.store.batches[b.ID] = *b
	if out, err := h.uc.ProcessBatch(ctx, h.tenantID, c.ID); err != nil || out != OutcomeSkipped {
		t.Fatalf("lote reservado por otro: outcome=%s err=%v", out, err)
	}
	if len(h.audience.calls) != 0 {
		t.Fatal("un lote reservado por otro no se toca")
	}
}

func TestTickStartsDueScheduled(t *testing.T) {
	h := newHarness()
	c := h.draft("Programada")
	version, at := 1, h.clock.now().Add(time.Hour)
	c.Status, c.TemplateVersion, c.ScheduledAt = domain.StatusScheduled, &version, &at
	h.store.campaigns[c.ID] = *c

	if err := h.uc.Tick(context.Background(), h.tenantID); err != nil {
		t.Fatal(err)
	}
	if h.campaign(c.ID).Status != domain.StatusScheduled {
		t.Fatal("no debe arrancar antes de su hora")
	}

	h.clock.advance(2 * time.Hour)
	if err := h.uc.Tick(context.Background(), h.tenantID); err != nil {
		t.Fatal(err)
	}
	camp := h.campaign(c.ID)
	if camp.StartedAt == nil || camp.Status != domain.StatusCompleted {
		t.Fatalf("la vencida arranca y, con audiencia vacia, termina en el mismo tick: %+v", camp)
	}
	if h.published("campaigns.campaign.started") == nil || h.published("campaigns.campaign.completed") == nil {
		t.Fatal("faltan los eventos started y completed")
	}
}
