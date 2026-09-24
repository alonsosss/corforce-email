//go:build integration

// Pruebas contra un Postgres real. Se ejecutan con:
//
//	TRANSACTIONAL_TEST_DSN=postgres://... go test -tags integration ./services/transactional/...
//
// Cada prueba aplica la outbox de plataforma y las migraciones del servicio DOS veces, que
// es lo que garantiza que toleran re-ejecutarse.
package postgres

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/transactional/internal/app"
	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
	"github.com/alonsosss/corforce-email/services/transactional/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
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

	migrations := []string{
		"migrations/tenant/canonical/platform/00_outbox.sql",
		"migrations/tenant/canonical/transactional/01_transactional.sql",
		"migrations/tenant/canonical/transactional/02_marketing_lane.sql",
		"migrations/tenant/canonical/transactional/03_test_sends.sql",
		"migrations/tenant/canonical/transactional/05_sending_domains_trigger.sql",
		"migrations/tenant/canonical/transactional/06_sending_ready.sql",
		"migrations/tenant/canonical/transactional/07_template_test_sends.sql",
		"migrations/tenant/canonical/transactional/08_raw_messages.sql",
	}
	applyMigrations(t, ctx, pool, append(migrations, migrations...)...)
	return db.WithPool(ctx, pool), pool, NewRepository(&db.ContextPool{})
}

// applyMigrations aplica, en orden, las migraciones dadas por su ruta desde la raiz del
// repositorio.
func applyMigrations(t *testing.T, ctx context.Context, pool *pgxpool.Pool, rels ...string) {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "..")
	for _, rel := range rels {
		sql, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("aplicar %s: %v", rel, err)
		}
	}
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
	var constraints int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint WHERE conname IN
		('messages_class_check', 'messages_marketing_check', 'submissions_class_check')`).Scan(&constraints); err != nil || constraints != 3 {
		t.Fatalf("restricciones del carril de marketing (una sola vez cada una): %d, %v", constraints, err)
	}
	var index *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('transactional.idx_transactional_messages_tenant_campaign')::text`).Scan(&index); err != nil || index == nil {
		t.Fatalf("indice por campana: %v", err)
	}
}

func marketingMessage(tenant uuid.UUID, to ...string) *domain.Message {
	m := newMessage(tenant, domain.StatusQueued, to...)
	campaign, contact := uuid.New(), uuid.New()
	m.Class, m.CampaignID, m.ContactID, m.Unsubscribable = domain.ClassMarketing, &campaign, &contact, true
	return m
}

func TestMarketingColumnsAndInvariants(t *testing.T) {
	ctx, _, repo := setup(t)
	tenant := uuid.New()
	m := marketingMessage(tenant, "ana@example.com")
	if err := repo.InsertMessage(ctx, m); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetMessage(ctx, tenant, m.ID)
	if err != nil || got.Class != domain.ClassMarketing || *got.CampaignID != *m.CampaignID || *got.ContactID != *m.ContactID {
		t.Fatalf("clase, campana y contacto sobreviven al viaje: %+v %v", got, err)
	}
	attr, err := repo.GetAttribution(ctx, tenant, m.ID)
	if err != nil || attr.Class != domain.ClassMarketing || *attr.CampaignID != *m.CampaignID || *attr.ContactID != *m.ContactID {
		t.Fatalf("GetAttribution: %+v %v", attr, err)
	}
	if _, err := repo.GetAttribution(ctx, uuid.New(), m.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("la atribucion no se ve desde otra empresa")
	}

	plain := newMessage(tenant, domain.StatusQueued, "eva@example.com")
	if err := repo.InsertMessage(ctx, plain); err != nil {
		t.Fatal(err)
	}
	if a, _ := repo.GetAttribution(ctx, tenant, plain.ID); a.Class != domain.ClassTransactional || a.CampaignID != nil || a.ContactID != nil {
		t.Fatalf("un mensaje sin clase es transaccional y sin campana: %+v", a)
	}
	list, total, err := repo.ListMessages(ctx, tenant, domain.MessageFilter{Class: domain.ClassMarketing}, 0, 10)
	if err != nil || total != 1 || list[0].ID != m.ID {
		t.Fatalf("filtro por clase: total=%d %v", total, err)
	}

	for name, mutate := range map[string]func(m *domain.Message){
		"clase desconocida":      func(m *domain.Message) { m.Class = "promo" },
		"marketing sin campana":  func(m *domain.Message) { m.CampaignID = nil },
		"marketing sin contacto": func(m *domain.Message) { m.ContactID = nil },
		"marketing sin baja":     func(m *domain.Message) { m.Unsubscribable = false },
		"marketing a dos":        func(m *domain.Message) { m.To = append(m.To, domain.Recipient{Email: "eva@example.com"}) },
		"marketing con copia":    func(m *domain.Message) { m.Cc = []domain.Recipient{{Email: "eva@example.com"}} },
	} {
		bad := marketingMessage(tenant, "luis@example.com")
		mutate(bad)
		if err := repo.InsertMessage(ctx, bad); err == nil {
			t.Errorf("%s: la base debe rechazarlo", name)
		}
	}
}

func TestSubmissionClassAndSuppressed(t *testing.T) {
	ctx, _, repo := setup(t)
	tenant := uuid.New()
	s := &domain.Submission{ID: uuid.New(), TenantID: tenant, IdempotencyKey: "lote-1", Class: domain.ClassMarketing,
		MessageIDs: []uuid.UUID{uuid.New()}, Suppressed: []domain.SuppressedRecipient{{Email: "eva@example.com", Reason: "complaint"}}}
	if ok, err := repo.InsertSubmission(ctx, s); err != nil || !ok {
		t.Fatalf("InsertSubmission: %v %v", ok, err)
	}
	got, err := repo.GetSubmission(ctx, tenant, "lote-1")
	if err != nil || got.Class != domain.ClassMarketing || len(got.Suppressed) != 1 || got.Suppressed[0].Reason != "complaint" {
		t.Fatalf("clase y suprimidos sobreviven al viaje: %+v %v", got, err)
	}
	if ok, err := repo.InsertSubmission(ctx, &domain.Submission{ID: uuid.New(), TenantID: tenant, IdempotencyKey: "api-1"}); err != nil || !ok {
		t.Fatalf("sin clase ni suprimidos (peticion transaccional): %v %v", ok, err)
	}
	if got, _ := repo.GetSubmission(ctx, tenant, "api-1"); got.Class != domain.ClassTransactional || got.Suppressed == nil {
		t.Fatalf("una peticion sin clase es transaccional: %+v", got)
	}
}

// Dobles minimos para correr el caso de uso del lote contra la base real.
type stubSuppression map[string]string

func (s stubSuppression) Check(_ context.Context, _ uuid.UUID, emails []string) ([]ports.Suppressed, error) {
	out := []ports.Suppressed{}
	for _, e := range emails {
		if reason, ok := s[strings.ToLower(e)]; ok {
			out = append(out, ports.Suppressed{Email: e, Reason: reason})
		}
	}
	return out, nil
}

func (stubSuppression) Add(context.Context, uuid.UUID, ports.SuppressionEntry) error { return nil }

type linkTemplates struct{}

func (linkTemplates) Render(_ context.Context, _ uuid.UUID, req ports.RenderRequest) (*ports.Rendered, error) {
	return &ports.Rendered{Subject: "Otono", HTML: `<a href="https://shop.example.com/oferta?id=1">Oferta</a><a href="` + req.Reserved.UnsubscribeURL + `">Baja</a>`, Version: *req.Version, Kind: domain.TemplateKindMarketing}, nil
}

type allowReputation struct{}

func (allowReputation) Authorize(_ context.Context, _ uuid.UUID, class string, _ int) (*ports.Authorization, error) {
	return &ports.Authorization{Allowed: true, Class: class}, nil
}

// failingRepo falla en la insercion numero failAt, a mitad de la transaccion del lote.
type failingRepo struct {
	*Repository
	failAt, n int
}

func (r *failingRepo) InsertMessage(ctx context.Context, m *domain.Message) error {
	r.n++
	if r.n == r.failAt {
		return errors.New("fallo a mitad del lote")
	}
	return r.Repository.InsertMessage(ctx, m)
}

func TestMarketingBatchWritesMessagesAndOutboxInOneTransaction(t *testing.T) {
	ctx, pool, repo := setup(t)
	tenant := uuid.New()
	if err := repo.UpsertSendingDomain(ctx, &domain.SendingDomain{TenantID: tenant, Domain: "shop.example.com", Status: "verified", Purpose: "both", UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	links, err := domain.NewLinkSigner("0123456789abcdef0123456789abcdef-integration", "https://app.example.com")
	if err != nil {
		t.Fatal(err)
	}
	newUC := func(r ports.Repository) *app.UseCase {
		return app.New(app.Deps{
			Repo: r, Events: NewOutboxPublisher(&db.ContextPool{}), Suppression: stubSuppression{"eva@example.com": "hard_bounce"},
			Templates: linkTemplates{}, Reputation: allowReputation{}, Links: links, Logger: zap.NewNop(),
			UTM: domain.NewLinkTagger([]string{"app.example.com"}),
		})
	}
	batch := func(key string) app.MarketingBatchCommand {
		cmd := app.MarketingBatchCommand{
			TenantID: tenant, Class: domain.ClassMarketing, CampaignID: uuid.New(), IdempotencyKey: key,
			From: domain.Recipient{Email: "news@shop.example.com", Name: "Tienda"}, TemplateID: uuid.New(), TemplateVersion: 2,
		}
		for _, e := range []string{"ana@example.com", "eva@example.com", "luis@example.com", "sara@example.com"} {
			cmd.Recipients = append(cmd.Recipients, app.MarketingRecipient{Email: e, ContactID: uuid.New()})
		}
		return cmd
	}
	counts := func(campaign uuid.UUID) (messages, outbox, submissions int) {
		t.Helper()
		_ = pool.QueryRow(ctx, `SELECT count(*) FROM transactional.messages WHERE tenant_id = $1 AND campaign_id = $2 AND class = 'marketing'`, tenant, campaign).Scan(&messages)
		_ = pool.QueryRow(ctx, `SELECT count(*) FROM platform.event_outbox o JOIN transactional.messages m
			ON m.id::text = o.payload->'data'->>'message_id'
			WHERE o.subject = 'transactional.marketing.queued' AND m.campaign_id = $1`, campaign).Scan(&outbox)
		_ = pool.QueryRow(ctx, `SELECT count(*) FROM transactional.submissions s WHERE s.tenant_id = $1 AND EXISTS (
			SELECT 1 FROM transactional.messages m WHERE m.submission_id = s.id AND m.campaign_id = $2)`, tenant, campaign).Scan(&submissions)
		return
	}

	// Un fallo en la tercera insercion deshace el lote entero: mensajes, peticion y eventos.
	failed := batch("lote-fallido")
	if _, err := newUC(&failingRepo{Repository: repo, failAt: 3}).CreateMarketingBatch(ctx, failed); err == nil {
		t.Fatal("se esperaba el fallo a mitad del lote")
	}
	var leftovers int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM transactional.messages WHERE campaign_id = $1`, failed.CampaignID).Scan(&leftovers)
	if _, err := repo.GetSubmission(ctx, tenant, "lote-fallido"); leftovers != 0 || !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("un lote fallido no deja nada: mensajes=%d peticion=%v", leftovers, err)
	}

	uc := newUC(repo)
	cmd := batch("lote-1")
	res, err := uc.CreateMarketingBatch(ctx, cmd)
	if err != nil || res.Accepted != 3 || len(res.Suppressed) != 1 {
		t.Fatalf("lote: %+v %v", res, err)
	}
	if m, o, s := counts(cmd.CampaignID); m != 3 || o != 3 || s != 1 {
		t.Fatalf("N filas de marketing y N eventos de outbox en la misma transaccion: mensajes=%d outbox=%d peticiones=%d", m, o, s)
	}
	stored, err := repo.GetMessage(ctx, tenant, res.MessageIDs[0])
	if err != nil || stored.HTML == nil {
		t.Fatalf("mensaje guardado: %v", err)
	}
	if !strings.Contains(*stored.HTML, `oferta?id=1&amp;utm_source=tienda&amp;utm_medium=email&amp;utm_campaign=`+cmd.CampaignID.String()+`"`) ||
		strings.Count(*stored.HTML, "utm_source") != 1 {
		t.Fatalf("se guarda el HTML con los UTM y la baja sin tocar: %s", *stored.HTML)
	}
	var outboxTenant string
	_ = pool.QueryRow(ctx, `SELECT DISTINCT tenant_id::text FROM platform.event_outbox WHERE payload->'data'->>'message_id' = $1`, res.MessageIDs[0].String()).Scan(&outboxTenant)
	if outboxTenant != tenant.String() {
		t.Fatalf("el evento lleva la empresa: %q", outboxTenant)
	}

	replay, err := uc.CreateMarketingBatch(ctx, cmd)
	if err != nil || !replay.Replayed || replay.Accepted != 3 || len(replay.Suppressed) != 1 || replay.MessageIDs[2] != res.MessageIDs[2] {
		t.Fatalf("repeticion: %+v %v", replay, err)
	}
	if m, o, s := counts(cmd.CampaignID); m != 3 || o != 3 || s != 1 {
		t.Fatalf("la repeticion no crea nada: mensajes=%d outbox=%d peticiones=%d", m, o, s)
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
	if list, _ := repo.ListSendingDomains(ctx, tenant); len(list) != 1 || list[0].SendingReady != nil {
		t.Fatal("ListSendingDomains: sin sending_ready la fila lo deja nulo")
	}
	notReady := false
	d.Status, d.SendingReady = "verified", &notReady
	if err := repo.UpsertSendingDomain(ctx, d); err != nil {
		t.Fatal(err)
	}
	d.SendingReady = nil
	if err := repo.UpsertSendingDomain(ctx, d); err != nil {
		t.Fatal(err)
	}
	if got, err := repo.GetSendingDomain(ctx, tenant, "shop.example.com"); err != nil || got.SendingReady == nil || *got.SendingReady || got.CanSend() {
		t.Fatalf("un evento sin sending_ready conserva el ultimo que dijo domain-service: %+v %v", got, err)
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
