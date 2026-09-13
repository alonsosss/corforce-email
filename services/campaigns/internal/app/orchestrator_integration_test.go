//go:build integration

// Pruebas del orquestador contra un Postgres real, con contacts y transactional
// sustituidos por los dobles de fakes_test.go:
//
//	CAMPAIGNS_TEST_DSN=postgres://... go test -tags integration ./services/campaigns/...
//
// Aplican la outbox de plataforma y la migracion del servicio DOS veces: la migracion
// debe tolerar re-ejecutarse.
package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/campaigns/internal/adapters/postgres"
	"github.com/alonsosss/corforce-email/services/campaigns/internal/domain"
	"github.com/alonsosss/corforce-email/services/campaigns/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// crashingBatches hace fallar el cierre del lote como si el proceso muriera entre el
// 202 de transactional y el commit.
type crashingBatches struct {
	ports.BatchRepository
	fail atomic.Int32
}

func (c *crashingBatches) MarkDelivered(ctx context.Context, tenantID, id uuid.UUID, accepted, suppressed int) (bool, error) {
	if c.fail.Add(-1) >= 0 {
		return false, errCrash
	}
	return c.BatchRepository.MarkDelivered(ctx, tenantID, id, accepted, suppressed)
}

var migrateOnce sync.Once

func setupDB(t *testing.T) (context.Context, *pgxpool.Pool, *harness, *crashingBatches) {
	t.Helper()
	dsn := os.Getenv("CAMPAIGNS_TEST_DSN")
	if dsn == "" {
		t.Skip("CAMPAIGNS_TEST_DSN no definida")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	var migErr error
	migrateOnce.Do(func() {
		_, file, _, _ := runtime.Caller(0)
		root := filepath.Join(filepath.Dir(file), "..", "..", "..", "..")
		for _, rel := range []string{
			"migrations/tenant/canonical/platform/00_outbox.sql",
			"migrations/tenant/canonical/campaigns/01_campaigns.sql",
			"migrations/tenant/canonical/platform/00_outbox.sql",
			"migrations/tenant/canonical/campaigns/01_campaigns.sql",
		} {
			sql, err := os.ReadFile(filepath.Join(root, rel))
			if err != nil {
				migErr = err
				return
			}
			if _, err := pool.Exec(context.Background(), string(sql)); err != nil {
				migErr = err
				return
			}
		}
	})
	if migErr != nil {
		t.Fatalf("aplicar migraciones: %v", migErr)
	}

	h := newHarness()
	h.clock.t = time.Now().UTC().Truncate(time.Microsecond)
	ctxPool := &db.ContextPool{}
	batches := &crashingBatches{BatchRepository: postgres.NewBatchRepository(ctxPool)}
	h.uc = New(Deps{
		Campaigns: postgres.NewCampaignRepository(ctxPool),
		Batches:   batches,
		Stats:     postgres.NewStatsRepository(ctxPool),
		Tx:        ctxPool,
		Events:    postgres.NewOutboxPublisher(ctxPool),
		Audience:  h.audience,
		Sender:    h.sender,
		Templates: h.templates,
		Config:    Config{BatchSize: domain.MaxBatchSize},
		Now:       h.clock.now,
	})
	return db.WithTenant(context.Background(), pool, h.tenantID.String()), pool, h, batches
}

func createSending(t *testing.T, ctx context.Context, h *harness, name string) *domain.Campaign {
	t.Helper()
	c, err := h.uc.Create(ctx, h.tenantID, domain.NewCampaignInput{
		Name: name, TemplateID: uuid.New(), FromEmail: "news@shop.example.com", FromName: "Tienda",
		Audience: domain.Audience{ListIDs: []uuid.UUID{uuid.New()}}, CreatedBy: uuid.New(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if c, err = h.uc.Start(ctx, h.tenantID, c.ID, nil); err != nil {
		t.Fatal(err)
	}
	return c
}

type batchRow struct {
	status     string
	hasPage    bool
	recipients int
	leased     bool
	accepted   int
	attempts   int
	cursorIn   *string
}

func readBatch(t *testing.T, pool *pgxpool.Pool, campaignID uuid.UUID, seq int) batchRow {
	t.Helper()
	var r batchRow
	err := pool.QueryRow(context.Background(),
		`SELECT status, page IS NOT NULL, recipients, lease_token IS NOT NULL, accepted, attempts, cursor_in
		   FROM campaigns.batches WHERE campaign_id = $1 AND seq = $2`, campaignID, seq,
	).Scan(&r.status, &r.hasPage, &r.recipients, &r.leased, &r.accepted, &r.attempts, &r.cursorIn)
	if err != nil {
		t.Fatalf("leer lote %d: %v", seq, err)
	}
	return r
}

func outboxCount(t *testing.T, pool *pgxpool.Pool, subject string, campaignID uuid.UUID) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM platform.event_outbox WHERE subject = $1 AND payload->'data'->>'campaign_id' = $2`,
		subject, campaignID.String()).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestIntegrationMigrationObjects(t *testing.T) {
	_, pool, _, _ := setupDB(t)
	for _, rel := range []string{"campaigns.campaigns", "campaigns.batches", "campaigns.processed_events",
		"campaigns.message_engagement", "platform.event_outbox"} {
		var name *string
		if err := pool.QueryRow(context.Background(), `SELECT to_regclass($1)::text`, rel).Scan(&name); err != nil || name == nil {
			t.Errorf("%s no existe tras aplicar la migracion dos veces: %v", rel, err)
		}
	}
	var triggers int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM pg_trigger WHERE tgname IN ('trg_campaigns_campaigns_updated', 'trg_campaigns_batches_updated')`,
	).Scan(&triggers); err != nil || triggers != 2 {
		t.Fatalf("triggers de updated_at: %d, %v", triggers, err)
	}
}

// TestIntegrationClaimBeforeSendAndCrashRecovery: el lote esta confirmado, con su
// pagina, antes de llamar a transactional; si el proceso cae tras el 202 y antes del
// commit, el reintento lleva la misma clave y no crea mensajes nuevos.
func TestIntegrationClaimBeforeSendAndCrashRecovery(t *testing.T) {
	ctx, pool, h, batches := setupDB(t)
	c := createSending(t, ctx, h, "Integracion caida "+uuid.NewString())
	h.audience.pages[""] = &ports.AudiencePage{
		Contacts:   []domain.Contact{contact("a@example.com"), contact("b@example.com"), contact("c@example.com")},
		NextCursor: strPtr("p2"),
	}
	h.audience.pages["p2"] = &ports.AudiencePage{Contacts: []domain.Contact{contact("d@example.com")}}

	var seenBeforeSend []batchRow
	h.audience.hook = func(q ports.AudienceQuery) {
		if q.Cursor == nil {
			seenBeforeSend = append(seenBeforeSend, readBatch(t, pool, c.ID, 1))
		}
	}
	h.sender.hook = func(r ports.BatchRequest) {
		if r.IdempotencyKey == domain.BatchIdempotencyKey(c.ID, 1) {
			seenBeforeSend = append(seenBeforeSend, readBatch(t, pool, c.ID, 1))
		}
	}
	batches.fail.Store(1)

	out, err := h.uc.ProcessBatch(ctx, h.tenantID, c.ID)
	if !errors.Is(err, errCrash) || out != OutcomeAborted {
		t.Fatalf("caida: outcome=%s err=%v", out, err)
	}
	if len(seenBeforeSend) != 2 {
		t.Fatalf("observaciones previas: %d", len(seenBeforeSend))
	}
	if claim := seenBeforeSend[0]; claim.status != "pending" || !claim.leased || claim.hasPage {
		t.Fatalf("antes de pedir la pagina el lote ya esta confirmado y reservado: %+v", claim)
	}
	if paged := seenBeforeSend[1]; paged.status != "pending" || !paged.hasPage || paged.recipients != 3 {
		t.Fatalf("antes de enviar la pagina ya esta guardada: %+v", paged)
	}
	if after := readBatch(t, pool, c.ID, 1); after.status != "pending" || !after.hasPage || !after.leased {
		t.Fatalf("tras la caida el lote sigue pendiente: %+v", after)
	}
	if h.sender.created != 3 {
		t.Fatalf("transactional acepto %d", h.sender.created)
	}

	if out, err := h.uc.ProcessBatch(ctx, h.tenantID, c.ID); err != nil || out != OutcomeSkipped {
		t.Fatalf("con la reserva viva: outcome=%s err=%v", out, err)
	}

	h.clock.advance(domain.BatchLease)
	if out, err := h.uc.ProcessBatch(ctx, h.tenantID, c.ID); err != nil || out != OutcomeDelivered {
		t.Fatalf("reanudacion: outcome=%s err=%v", out, err)
	}
	if len(h.sender.calls) != 2 || h.sender.calls[0].IdempotencyKey != h.sender.calls[1].IdempotencyKey {
		t.Fatalf("misma clave en el reintento: %+v", h.sender.calls)
	}
	if h.sender.created != 3 || len(h.audience.calls) != 1 {
		t.Fatalf("sin duplicados ni nueva consulta: creados=%d consultas=%d", h.sender.created, len(h.audience.calls))
	}
	if b := readBatch(t, pool, c.ID, 1); b.status != "delivered" || b.hasPage || b.leased || b.accepted != 3 {
		t.Fatalf("lote 1 cerrado: %+v", b)
	}

	if out, err := h.uc.ProcessBatch(ctx, h.tenantID, c.ID); err != nil || out != OutcomeCompleted {
		t.Fatalf("ultimo lote: outcome=%s err=%v", out, err)
	}
	if b := readBatch(t, pool, c.ID, 2); b.cursorIn == nil || *b.cursorIn != "p2" || b.status != "delivered" {
		t.Fatalf("lote 2: %+v", b)
	}
	got, err := h.uc.Get(ctx, h.tenantID, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.StatusCompleted || got.Counters.Targeted != 4 || got.Counters.Accepted != 4 {
		t.Fatalf("campana: status=%s counters=%+v", got.Status, got.Counters)
	}
	if outboxCount(t, pool, "campaigns.campaign.started", c.ID) != 1 || outboxCount(t, pool, "campaigns.campaign.completed", c.ID) != 1 {
		t.Fatal("los eventos started y completed deben estar en la outbox, una vez cada uno")
	}
}

// TestIntegrationSkipLocked: una campana bloqueada por otra transaccion se salta sin
// esperar, y dos goroutines sobre la misma campana envian un solo lote.
func TestIntegrationSkipLocked(t *testing.T) {
	ctx, pool, h, _ := setupDB(t)
	c := createSending(t, ctx, h, "Integracion bloqueo "+uuid.NewString())
	h.audience.pages[""] = &ports.AudiencePage{Contacts: []domain.Contact{contact("a@example.com")}}

	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(context.Background(), `SELECT 1 FROM campaigns.campaigns WHERE id = $1 FOR UPDATE`, c.ID); err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	out, err := h.uc.ProcessBatch(ctx, h.tenantID, c.ID)
	if err != nil || out != OutcomeSkipped || time.Since(began) > 2*time.Second {
		t.Fatalf("con la fila bloqueada se salta sin esperar: outcome=%s err=%v en %s", out, err, time.Since(began))
	}
	if len(h.audience.calls) != 0 {
		t.Fatal("no se consulto nada")
	}
	_ = tx.Rollback(context.Background())

	var calls atomic.Int32
	entered, release := make(chan struct{}, 2), make(chan struct{})
	h.sender.hook = func(ports.BatchRequest) {
		calls.Add(1)
		entered <- struct{}{}
		<-release
	}
	results := make(chan Outcome, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			out, err := h.uc.ProcessBatch(ctx, h.tenantID, c.ID)
			if err != nil {
				t.Error(err)
			}
			results <- out
		}()
	}
	close(start)
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("ninguna goroutine llego a enviar")
	}
	select {
	case out := <-results:
		if out != OutcomeSkipped {
			t.Fatalf("la segunda goroutine debe saltar la campana: %s", out)
		}
	case <-time.After(10 * time.Second):
		close(release)
		t.Fatal("la segunda goroutine se quedo esperando o intento enviar")
	}
	close(release)
	wg.Wait()
	if out := <-results; out != OutcomeCompleted {
		t.Fatalf("la primera termina la campana: %s", out)
	}
	if calls.Load() != 1 {
		t.Fatalf("envios: %d", calls.Load())
	}
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM campaigns.batches WHERE campaign_id = $1`, c.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("lotes: %d %v", n, err)
	}
}

// TestIntegrationPauseWhileFetching: si una persona pausa la campana mientras se pide la
// pagina, el lote no se envia y queda libre para cuando se reanude.
func TestIntegrationPauseWhileFetching(t *testing.T) {
	ctx, pool, h, _ := setupDB(t)
	c := createSending(t, ctx, h, "Integracion pausa "+uuid.NewString())
	h.audience.pages[""] = &ports.AudiencePage{Contacts: []domain.Contact{contact("a@example.com")}}
	h.audience.hook = func(ports.AudienceQuery) {
		if _, err := h.uc.Pause(ctx, h.tenantID, c.ID); err != nil {
			t.Errorf("pausar: %v", err)
		}
	}
	out, err := h.uc.ProcessBatch(ctx, h.tenantID, c.ID)
	if err != nil || out != OutcomeAborted {
		t.Fatalf("outcome=%s err=%v", out, err)
	}
	if len(h.sender.calls) != 0 {
		t.Fatal("una campana pausada no envia el lote en vuelo que aun no tenia pagina")
	}
	if b := readBatch(t, pool, c.ID, 1); b.status != "pending" || b.hasPage || b.leased {
		t.Fatalf("el lote queda pendiente, sin pagina y libre: %+v", b)
	}
	h.audience.hook = nil
	if _, err := h.uc.Resume(ctx, h.tenantID, c.ID); err != nil {
		t.Fatal(err)
	}
	if out, err := h.uc.ProcessBatch(ctx, h.tenantID, c.ID); err != nil || out != OutcomeCompleted {
		t.Fatalf("tras reanudar: outcome=%s err=%v", out, err)
	}
	if h.sender.calls[0].IdempotencyKey != domain.BatchIdempotencyKey(c.ID, 1) {
		t.Fatalf("clave: %s", h.sender.calls[0].IdempotencyKey)
	}
}

func TestIntegrationFailuresAndRateLimit(t *testing.T) {
	ctx, pool, h, _ := setupDB(t)
	c := createSending(t, ctx, h, "Integracion fallos "+uuid.NewString())
	h.audience.pages[""] = &ports.AudiencePage{Contacts: []domain.Contact{contact("a@example.com")}}
	h.sender.errs = []error{unavailable(), &ports.RateLimitedError{RetryAfter: time.Minute}}

	if out, err := h.uc.ProcessBatch(ctx, h.tenantID, c.ID); err != nil || out != OutcomeRetrying {
		t.Fatalf("503: outcome=%s err=%v", out, err)
	}
	if b := readBatch(t, pool, c.ID, 1); b.attempts != 1 || b.leased {
		t.Fatalf("503 cuenta un intento y libera la reserva: %+v", b)
	}
	h.clock.advance(domain.RetryBackoff(1))
	if out, err := h.uc.ProcessBatch(ctx, h.tenantID, c.ID); err != nil || out != OutcomeRateLimited {
		t.Fatalf("429: outcome=%s err=%v", out, err)
	}
	got, _ := h.uc.Get(ctx, h.tenantID, c.ID)
	if got.ResumeAfter == nil || !got.ResumeAfter.Equal(h.clock.now().Add(time.Minute)) {
		t.Fatalf("resume_after: %v", got.ResumeAfter)
	}
	if b := readBatch(t, pool, c.ID, 1); b.attempts != 1 {
		t.Fatalf("429 no cuenta intento: %+v", b)
	}
	if out, _ := h.uc.ProcessBatch(ctx, h.tenantID, c.ID); out != OutcomeSkipped {
		t.Fatalf("antes de Retry-After: %s", out)
	}
	h.clock.advance(time.Minute)
	h.sender.errs = []error{&ports.RejectedError{Message: "plantilla que no es de marketing"}}
	if out, err := h.uc.ProcessBatch(ctx, h.tenantID, c.ID); err != nil || out != OutcomeFailed {
		t.Fatalf("422: outcome=%s err=%v", out, err)
	}
	if b := readBatch(t, pool, c.ID, 1); b.status != "failed" || b.hasPage {
		t.Fatalf("lote rechazado: %+v", b)
	}
	if got, _ = h.uc.Get(ctx, h.tenantID, c.ID); got.Status != domain.StatusFailed || got.FailureReason == "" {
		t.Fatalf("campana: %+v", got)
	}
	if outboxCount(t, pool, "campaigns.campaign.failed", c.ID) != 1 {
		t.Fatal("falta el evento failed en la outbox")
	}
}

func TestIntegrationStatsAndRepository(t *testing.T) {
	ctx, pool, h, _ := setupDB(t)
	c := createSending(t, ctx, h, "Integracion stats "+uuid.NewString())
	m1, m2 := uuid.New(), uuid.New()
	events := []domain.DeliveryEvent{
		deliveryEvent(h, uuid.NewString(), c.ID, domain.KindDelivered, &m1),
		deliveryEvent(h, uuid.NewString(), c.ID, domain.KindOpened, &m1),
		deliveryEvent(h, uuid.NewString(), c.ID, domain.KindOpened, &m1),
		deliveryEvent(h, uuid.NewString(), c.ID, domain.KindOpened, &m2),
		deliveryEvent(h, uuid.NewString(), c.ID, domain.KindClicked, &m1),
	}
	events = append(events, events[0])
	for _, ev := range events {
		if _, err := h.uc.RecordDeliveryEvent(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	got, err := h.uc.Get(ctx, h.tenantID, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Counters.Delivered != 1 || got.Counters.Opened != 2 || got.Counters.Clicked != 1 {
		t.Fatalf("contadores: %+v", got.Counters)
	}
	ghostMsg := uuid.New()
	ghost := deliveryEvent(h, uuid.NewString(), uuid.New(), domain.KindOpened, &ghostMsg)
	if _, err := h.uc.RecordDeliveryEvent(ctx, ghost); !errors.Is(err, domain.ErrCampaignNotFound) {
		t.Fatalf("campana inexistente: %v", err)
	}
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM campaigns.processed_events WHERE event_id = $1`, ghost.EventID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("el evento de una campana inexistente no queda procesado: %d %v", n, err)
	}

	other, err := h.uc.Create(ctx, h.tenantID, domain.NewCampaignInput{
		Name: "Integracion otra " + uuid.NewString(), TemplateID: uuid.New(), FromEmail: "news@shop.example.com",
		Audience: domain.Audience{SegmentIDs: []uuid.UUID{uuid.New()}}, CreatedBy: uuid.New(),
	})
	if err != nil {
		t.Fatal(err)
	}
	upper := domain.Patch{Name: ptrString(stringsToUpper(c.Name))}
	if _, err := h.uc.Update(ctx, h.tenantID, other.ID, upper); !errors.Is(err, domain.ErrNameTaken) {
		t.Fatalf("nombre repetido sin distinguir mayusculas: %v", err)
	}
	list, total, err := h.uc.List(ctx, h.tenantID, ports.ListFilter{Status: domain.StatusDraft, Search: "otra", Page: 1, PerPage: 10})
	if err != nil || total != 1 || len(list) != 1 || list[0].ID != other.ID || len(list[0].Audience.SegmentIDs) != 1 {
		t.Fatalf("listado: total=%d list=%+v err=%v", total, list, err)
	}

	h.clock.advance(domain.ProcessedEventRetention + time.Hour)
	if pruned, err := h.uc.PruneProcessedEvents(ctx, h.tenantID); err != nil || pruned < 5 {
		t.Fatalf("poda: %d %v", pruned, err)
	}

	if _, err := h.uc.Cancel(ctx, h.tenantID, c.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.uc.Delete(ctx, h.tenantID, c.ID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM campaigns.message_engagement WHERE campaign_id = $1`, c.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("borrar la campana arrastra sus aperturas: %d %v", n, err)
	}

	scheduled, err := h.uc.Create(ctx, h.tenantID, domain.NewCampaignInput{
		Name: "Integracion programada " + uuid.NewString(), TemplateID: uuid.New(), FromEmail: "news@shop.example.com",
		Audience: domain.Audience{ListIDs: []uuid.UUID{uuid.New()}}, CreatedBy: uuid.New(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.uc.Schedule(ctx, h.tenantID, scheduled.ID, h.clock.now().Add(2*time.Minute), nil); err != nil {
		t.Fatal(err)
	}
	h.clock.advance(3 * time.Minute)
	if err := h.uc.Tick(ctx, h.tenantID); err != nil {
		t.Fatal(err)
	}
	if got, _ := h.uc.Get(ctx, h.tenantID, scheduled.ID); got.Status != domain.StatusCompleted || got.StartedAt == nil {
		t.Fatalf("la programada vencida arranca y, sin audiencia, termina: %+v", got)
	}
}

func ptrString(s string) *string { return &s }

func stringsToUpper(s string) string {
	out := []rune(s)
	for i, r := range out {
		if r >= 'a' && r <= 'z' {
			out[i] = r - 'a' + 'A'
		}
	}
	return string(out)
}
