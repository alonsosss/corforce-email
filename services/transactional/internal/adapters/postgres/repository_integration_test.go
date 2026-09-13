//go:build integration

// Pruebas contra un Postgres real. Se ejecutan con:
//
//	TRANSACTIONAL_TEST_DSN=postgres://... go test -tags integration ./services/transactional/...
//
// Cada prueba aplica la outbox de plataforma y la migracion del servicio DOS veces, que es
// lo que garantiza que la migracion tolera re-ejecutarse.
package postgres

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func setup(t *testing.T) (context.Context, *pgxpool.Pool, *Repository) {
	t.Helper()
	dsn := os.Getenv("TRANSACTIONAL_TEST_DSN")
	if dsn == "" {
		t.Skip("TRANSACTIONAL_TEST_DSN no definida")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "..")
	for _, rel := range []string{
		"migrations/tenant/canonical/platform/00_outbox.sql",
		"migrations/tenant/canonical/transactional/01_transactional.sql",
		"migrations/tenant/canonical/transactional/01_transactional.sql",
	} {
		sql, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("aplicar %s: %v", rel, err)
		}
	}
	return db.WithPool(ctx, pool), pool, NewRepository(&db.ContextPool{})
}

func ptr[T any](v T) *T { return &v }

func newMessage(tenant uuid.UUID, status string, to ...string) *domain.Message {
	now := time.Now().UTC().Truncate(time.Microsecond)
	m := &domain.Message{
		ID: uuid.New(), TenantID: tenant, FromEmail: "no-reply@shop.example.com", FromName: "Tienda",
		Subject: "Pedido", HTML: ptr("<p>ok</p>"), Status: status,
		Variables: map[string]any{"order": "A-1", "total": 10.5},
		Headers:   map[string]string{"X-Order-Id": "A-1"},
		Tags:      map[string]string{"flow": "order"},
		CreatedAt: now, UpdatedAt: now,
	}
	for _, e := range to {
		m.To = append(m.To, domain.Recipient{Email: e, Name: "Cliente"})
	}
	return m
}

func TestMigrationObjects(t *testing.T) {
	ctx, pool, _ := setup(t)
	for _, rel := range []string{"transactional.messages", "transactional.events", "transactional.submissions",
		"transactional.sending_domains", "transactional.unsubscribes", "platform.event_outbox"} {
		var name *string
		if err := pool.QueryRow(ctx, `SELECT to_regclass($1)::text`, rel).Scan(&name); err != nil || name == nil {
			t.Errorf("%s no existe tras aplicar la migracion dos veces: %v", rel, err)
		}
	}
	var triggers int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_trigger WHERE tgname = 'trg_transactional_messages_updated'`).Scan(&triggers); err != nil || triggers != 1 {
		t.Fatalf("trigger de updated_at: %d, %v", triggers, err)
	}
}

func TestMessageRoundTripAndLifecycle(t *testing.T) {
	ctx, _, repo := setup(t)
	tenant := uuid.New()
	m := newMessage(tenant, domain.StatusQueued, "Ana@example.com", "eva@example.com")
	m.Cc, m.Bcc = nil, nil
	if err := repo.InsertMessage(ctx, m); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetMessage(ctx, tenant, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.To) != 2 || got.To[0].Name != "Cliente" || got.Variables["order"] != "A-1" || got.Headers["X-Order-Id"] != "A-1" || got.Tags["flow"] != "order" {
		t.Fatalf("el JSON no sobrevive al viaje: %+v", got)
	}
	if got.Cc == nil || len(got.Cc) != 0 {
		t.Fatal("cc vacio se guarda como lista vacia")
	}
	if _, err := repo.GetMessage(ctx, uuid.New(), m.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("un mensaje no se ve desde otra empresa")
	}

	if err := repo.MarkSent(ctx, tenant, m.ID, "0100-ses", time.Now()); err != nil {
		t.Fatal(err)
	}
	if ok, err := repo.TransitionStatus(ctx, tenant, m.ID, domain.StatusDelivered, []string{domain.StatusSent}); err != nil || !ok {
		t.Fatalf("sent -> delivered: %v %v", ok, err)
	}
	if ok, _ := repo.TransitionStatus(ctx, tenant, m.ID, domain.StatusRejected, []string{domain.StatusSent}); ok {
		t.Fatal("una transicion desde un estado que no es el actual no aplica")
	}
	got, _ = repo.GetMessage(ctx, tenant, m.ID)
	if got.Status != domain.StatusDelivered || got.SESMessageID == nil || *got.SESMessageID != "0100-ses" || got.Attempts != 1 || !got.UpdatedAt.After(m.UpdatedAt) {
		t.Fatalf("estado final: %+v", got)
	}

	n, err := repo.RecordAttempt(ctx, tenant, m.ID, "throttled")
	if err != nil || n != 2 {
		t.Fatalf("RecordAttempt: %d %v", n, err)
	}

	other := newMessage(tenant, domain.StatusFailed, "luis@example.com")
	if err := repo.InsertMessage(ctx, other); err != nil {
		t.Fatal(err)
	}
	list, total, err := repo.ListMessages(ctx, tenant, domain.MessageFilter{To: "ana@example.com"}, 0, 10)
	if err != nil || total != 1 || len(list) != 1 || list[0].ID != m.ID {
		t.Fatalf("filtro por destinatario sin distinguir mayusculas: total=%d err=%v", total, err)
	}
	list, total, _ = repo.ListMessages(ctx, tenant, domain.MessageFilter{Status: domain.StatusFailed}, 0, 10)
	if total != 1 || list[0].ID != other.ID {
		t.Fatal("filtro por estado")
	}
	from := time.Now().Add(-time.Hour)
	_, total, _ = repo.ListMessages(ctx, tenant, domain.MessageFilter{From: "no-reply@shop.example.com", DateFrom: &from}, 0, 1)
	if total != 2 {
		t.Fatalf("filtro por remitente y fecha: total=%d", total)
	}
	stats, err := repo.CountByStatus(ctx, tenant, from, time.Now().Add(time.Hour))
	if err != nil || len(stats) != 2 {
		t.Fatalf("estadisticas por estado: %+v %v", stats, err)
	}
}

func TestStatusCheckConstraint(t *testing.T) {
	ctx, _, repo := setup(t)
	if err := repo.InsertMessage(ctx, newMessage(uuid.New(), "enviado", "ana@example.com")); err == nil {
		t.Fatal("un estado fuera de la lista debe rechazarse")
	}
	m := newMessage(uuid.New(), domain.StatusQueued, "ana@example.com")
	m.HTML = nil
	if err := repo.InsertMessage(ctx, m); err == nil {
		t.Fatal("un mensaje sin plantilla ni cuerpo debe rechazarse")
	}
}

func TestSubmissionUniquePerTenant(t *testing.T) {
	ctx, _, repo := setup(t)
	tenant := uuid.New()
	ids := []uuid.UUID{uuid.New(), uuid.New()}
	s := &domain.Submission{ID: uuid.New(), TenantID: tenant, IdempotencyKey: "order-1", MessageIDs: ids}
	if ok, err := repo.InsertSubmission(ctx, s); err != nil || !ok {
		t.Fatalf("primera: %v %v", ok, err)
	}
	dup := &domain.Submission{ID: uuid.New(), TenantID: tenant, IdempotencyKey: "order-1", MessageIDs: []uuid.UUID{uuid.New()}}
	if ok, err := repo.InsertSubmission(ctx, dup); err != nil || ok {
		t.Fatalf("la misma clave en la misma empresa no entra: %v %v", ok, err)
	}
	if ok, err := repo.InsertSubmission(ctx, &domain.Submission{ID: uuid.New(), TenantID: uuid.New(), IdempotencyKey: "order-1"}); err != nil || !ok {
		t.Fatalf("la misma clave en otra empresa si entra (y sin mensajes no rompe el NOT NULL): %v %v", ok, err)
	}
	got, err := repo.GetSubmission(ctx, tenant, "order-1")
	if err != nil || len(got.MessageIDs) != 2 || got.MessageIDs[1] != ids[1] {
		t.Fatalf("GetSubmission: %+v %v", got, err)
	}
	if _, err := repo.GetSubmission(ctx, tenant, "otra"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("clave inexistente")
	}
}

func TestEventsIdempotentBySNSMessageID(t *testing.T) {
	ctx, _, repo := setup(t)
	tenant := uuid.New()
	m := newMessage(tenant, domain.StatusSent, "ana@example.com")
	if err := repo.InsertMessage(ctx, m); err != nil {
		t.Fatal(err)
	}
	snsID := "sns-" + uuid.New().String()
	ev := func(sns *string) *domain.Event {
		return &domain.Event{ID: uuid.New(), TenantID: tenant, MessageID: m.ID, Type: domain.EventBounce,
			Recipient: "ana@example.com", Detail: map[string]any{"reason": "x"}, SNSMessageID: sns, OccurredAt: time.Now(), CreatedAt: time.Now()}
	}
	if ok, err := repo.InsertEvent(ctx, ev(&snsID)); err != nil || !ok {
		t.Fatalf("primer evento: %v %v", ok, err)
	}
	if ok, err := repo.InsertEvent(ctx, ev(&snsID)); err != nil || ok {
		t.Fatalf("la misma notificacion SNS no entra dos veces: %v %v", ok, err)
	}
	for i := 0; i < 2; i++ {
		if ok, err := repo.InsertEvent(ctx, ev(nil)); err != nil || !ok {
			t.Fatalf("los eventos propios sin sns_message_id no chocan entre si: %v %v", ok, err)
		}
	}
	unknown := ev(nil)
	unknown.MessageID = uuid.New()
	if _, err := repo.InsertEvent(ctx, unknown); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("evento de un mensaje inexistente: %v", err)
	}
	list, err := repo.ListEvents(ctx, tenant, m.ID)
	if err != nil || len(list) != 3 || list[0].Detail["reason"] != "x" {
		t.Fatalf("ListEvents: %d %v", len(list), err)
	}
}

func TestReleaseDueSkipsLockedRows(t *testing.T) {
	ctx, pool, repo := setup(t)
	tenant := uuid.New()
	now := time.Now().UTC()
	var due []uuid.UUID
	for i, at := range []time.Duration{-2 * time.Minute, -time.Minute, time.Hour} {
		m := newMessage(tenant, domain.StatusAccepted, "ana@example.com")
		m.ScheduledAt = ptr(now.Add(at))
		if err := repo.InsertMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
		if i < 2 {
			due = append(due, m.ID)
		}
	}

	tx1, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx1.Rollback(ctx) }()
	first, err := repo.ReleaseDue(db.WithTx(ctx, tx1), tenant, now, 1)
	if err != nil || len(first) != 1 || first[0] != due[0] {
		t.Fatalf("la primera replica toma el mas antiguo: %v %v", first, err)
	}

	var second []uuid.UUID
	if err := repo.Transact(ctx, func(ctx context.Context) error {
		var err error
		second, err = repo.ReleaseDue(ctx, tenant, now, 10)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || second[0] != due[1] {
		t.Fatalf("la segunda replica salta la fila bloqueada: %v", second)
	}
	if err := tx1.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range due {
		if m, _ := repo.GetMessage(ctx, tenant, id); m.Status != domain.StatusQueued {
			t.Fatalf("%s deberia estar en queued", id)
		}
	}
	var accepted int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM transactional.messages WHERE tenant_id = $1 AND status = 'accepted'`, tenant).Scan(&accepted)
	if accepted != 1 {
		t.Fatal("el programado futuro sigue esperando")
	}
}

func TestOutboxEnqueueFollowsTransaction(t *testing.T) {
	ctx, pool, repo := setup(t)
	pub := NewOutboxPublisher(&db.ContextPool{})
	tenant := uuid.New()
	count := func(id uuid.UUID) (msgs, outbox int) {
		_ = pool.QueryRow(ctx, `SELECT count(*) FROM transactional.messages WHERE id = $1`, id).Scan(&msgs)
		_ = pool.QueryRow(ctx, `SELECT count(*) FROM platform.event_outbox WHERE payload->'data'->>'message_id' = $1`, id.String()).Scan(&outbox)
		return
	}

	failed := newMessage(tenant, domain.StatusQueued, "ana@example.com")
	err := repo.Transact(ctx, func(ctx context.Context) error {
		if err := repo.InsertMessage(ctx, failed); err != nil {
			return err
		}
		if err := pub.Publish(ctx, "transactional.message.queued", tenant, map[string]any{"tenant_id": tenant.String(), "message_id": failed.ID.String()}); err != nil {
			return err
		}
		return errors.New("fallo posterior")
	})
	if err == nil {
		t.Fatal("se esperaba el error de la transaccion")
	}
	if m, o := count(failed.ID); m != 0 || o != 0 {
		t.Fatalf("un rollback retira mensaje y evento juntos: mensajes=%d outbox=%d", m, o)
	}

	ok := newMessage(tenant, domain.StatusQueued, "ana@example.com")
	if err := repo.Transact(ctx, func(ctx context.Context) error {
		if err := repo.InsertMessage(ctx, ok); err != nil {
			return err
		}
		return pub.Publish(ctx, "transactional.message.queued", tenant, map[string]any{"tenant_id": tenant.String(), "message_id": ok.ID.String()})
	}); err != nil {
		t.Fatal(err)
	}
	if m, o := count(ok.ID); m != 1 || o != 1 {
		t.Fatalf("un commit deja mensaje y evento: mensajes=%d outbox=%d", m, o)
	}
	var subject, outboxTenant string
	_ = pool.QueryRow(ctx, `SELECT subject, tenant_id::text FROM platform.event_outbox WHERE payload->'data'->>'message_id' = $1`, ok.ID.String()).Scan(&subject, &outboxTenant)
	if subject != "transactional.message.queued" || outboxTenant != tenant.String() {
		t.Fatalf("fila de outbox: %s %s", subject, outboxTenant)
	}
}

func TestSendingDomainsAndUnsubscribes(t *testing.T) {
	ctx, _, repo := setup(t)
	tenant := uuid.New()
	d := &domain.SendingDomain{TenantID: tenant, Domain: "shop.example.com", Status: "verified", Purpose: "sending", UpdatedAt: time.Now()}
	if err := repo.UpsertSendingDomain(ctx, d); err != nil {
		t.Fatal(err)
	}
	d.Status = "failed"
	if err := repo.UpsertSendingDomain(ctx, d); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetSendingDomain(ctx, tenant, "shop.example.com")
	if err != nil || got.Status != "failed" {
		t.Fatalf("el upsert actualiza el estado: %+v %v", got, err)
	}
	if list, _ := repo.ListSendingDomains(ctx, tenant); len(list) != 1 {
		t.Fatal("ListSendingDomains")
	}
	if err := repo.UpsertSendingDomain(ctx, &domain.SendingDomain{TenantID: tenant, Domain: "Mayus.example.com", Status: "verified", Purpose: "both", UpdatedAt: time.Now()}); err == nil {
		t.Fatal("la proyeccion exige dominios en minusculas")
	}
	if err := repo.DeleteSendingDomain(ctx, tenant, "shop.example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetSendingDomain(ctx, tenant, "shop.example.com"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("tras deleted no queda en la proyeccion")
	}

	u := &domain.Unsubscribe{ID: uuid.New(), TenantID: tenant, Email: "ana@example.com", MessageID: uuid.New(), CreatedAt: time.Now()}
	if ok, err := repo.InsertUnsubscribe(ctx, u); err != nil || !ok {
		t.Fatalf("primera baja: %v %v", ok, err)
	}
	u.ID = uuid.New()
	if ok, err := repo.InsertUnsubscribe(ctx, u); err != nil || ok {
		t.Fatalf("la baja es idempotente por mensaje y persona: %v %v", ok, err)
	}
}
