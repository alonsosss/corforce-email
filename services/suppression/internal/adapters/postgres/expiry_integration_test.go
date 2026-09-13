//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	supoutbox "github.com/alonsosss/corforce-email/services/suppression/internal/adapters/outbox"
	"github.com/alonsosss/corforce-email/services/suppression/internal/app"
	"github.com/alonsosss/corforce-email/services/suppression/internal/domain"
	"github.com/alonsosss/corforce-email/services/suppression/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// tempDatabase crea en la instancia de SUPPRESSION_TEST_DSN una base vacia que la prueba
// borra al terminar: las pruebas de migracion necesitan partir del esquema anterior.
func tempDatabase(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	dsn := integrationEnv(t, "SUPPRESSION_TEST_DSN")
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := "suppression_migration_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec(ctx, `CREATE DATABASE `+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(ctx, `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`); err != nil {
			t.Errorf("borrar la base temporal %s: %v", name, err)
		}
		admin.Close(ctx)
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// Sobre una base con las migraciones anteriores y datos, 04_expiry_announced.sql no cambia
// ninguna fila, deja la marca vacia (las caducadas de antes se anuncian en la primera
// pasada) y crea el indice parcial del barrido; repetida, no cambia nada.
func TestMigracionDeCaducidadAnunciada(t *testing.T) {
	ctx := context.Background()
	pool := tempDatabase(t, ctx)
	migrations := suppressionMigrations(t)
	var previous, rest []string
	for _, m := range migrations {
		if filepath.Base(m) < "04_expiry_announced.sql" {
			previous = append(previous, m)
		} else {
			rest = append(rest, m)
		}
	}
	if len(previous) != 3 || len(rest) == 0 || filepath.Base(rest[0]) != "04_expiry_announced.sql" {
		t.Fatalf("migraciones: %v / %v", previous, rest)
	}
	applyFiles(t, ctx, pool, append([]string{filepath.Join(repoRoot(t), "migrations", "tenant", "canonical", "platform", "00_outbox.sql")}, previous...)...)

	tenant := uuid.New()
	past := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	future := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `INSERT INTO suppression.entries (tenant_id, email, reason, source, expires_at) VALUES
		($1, 'caducada@example.com', 'manual', 'api', $2),
		($1, 'vigente@example.com', 'manual', 'api', $3),
		($1, 'baja@example.com', 'unsubscribe', 'transactional', NULL)`, tenant, past, future); err != nil {
		t.Fatal(err)
	}
	before := snapshotEntries(t, ctx, pool)

	applyFiles(t, ctx, pool, append(rest, rest...)...)

	if after := snapshotEntries(t, ctx, pool); !reflect.DeepEqual(before, after) {
		t.Fatalf("la migracion cambio filas:\nantes:   %v\ndespues: %v", before, after)
	}
	var marked int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM suppression.entries WHERE announced_expires_at IS NOT NULL`).Scan(&marked); err != nil || marked != 0 {
		t.Fatalf("ninguna fila existente queda anunciada: %d %v", marked, err)
	}
	var def string
	if err := pool.QueryRow(ctx, `SELECT indexdef FROM pg_indexes
		WHERE schemaname = 'suppression' AND indexname = 'idx_suppression_entries_expiry_pending'`).Scan(&def); err != nil {
		t.Fatalf("indice del barrido: %v", err)
	}
	if !strings.Contains(def, "(tenant_id, expires_at, id)") || !strings.Contains(def, "announced_expires_at IS DISTINCT FROM expires_at") {
		t.Fatalf("definicion del indice: %s", def)
	}

	repo := NewRepository(&db.ContextPool{})
	pending, err := repo.ListExpiryPending(db.WithPool(ctx, pool), tenant, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), nil, 10)
	if err != nil || len(pending) != 1 || pending[0].Email != "caducada@example.com" {
		t.Fatalf("la caducada de antes de la migracion queda pendiente: %+v %v", pending, err)
	}
}

// expiredEvents lee de la outbox los anuncios de caducidad de la empresa, en orden de
// alta.
func expiredEvents(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tenant uuid.UUID) []map[string]interface{} {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT payload FROM platform.event_outbox
		WHERE tenant_id = $1 AND subject = $2 ORDER BY created_at, id`, tenant, supoutbox.SubjectEntryExpired)
	if err != nil {
		t.Fatal(err)
	}
	payloads, err := pgx.CollectRows(rows, pgx.RowTo[[]byte])
	if err != nil {
		t.Fatal(err)
	}
	out := make([]map[string]interface{}, len(payloads))
	for i, p := range payloads {
		var evt struct {
			Data map[string]interface{} `json:"data"`
		}
		if err := json.Unmarshal(p, &evt); err != nil {
			t.Fatal(err)
		}
		out[i] = evt.Data
	}
	return out
}

// El barrido completo contra la base, con el reloj del caso de uso fijado (ninguna fecha
// real interviene): nada antes de la hora; a la hora, dos replicas a la vez anuncian una
// sola vez, en la transaccion que marca la fila; otra pasada no repite; la renovacion
// vuelve a anunciarse al caducar; otra empresa no se toca.
func TestBarridoDeCaducidadContraLaBase(t *testing.T) {
	pool, ctx := testPool(t)
	cp := &db.ContextPool{}
	var (
		mu    sync.Mutex
		clock = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	)
	now := func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	advance := func(d time.Duration) { mu.Lock(); defer mu.Unlock(); clock = clock.Add(d) }
	replica := func() *app.UseCase {
		return app.New(app.Deps{
			Entries: NewRepository(cp), Imports: NewImportRepository(cp), Tx: cp,
			Events: supoutbox.NewPublisher(cp), Logger: zap.NewNop(), Now: now,
		})
	}
	uc := replica()
	tenant, other := uuid.New(), uuid.New()
	manual := func(tenantID uuid.UUID, email string, d time.Duration) {
		t.Helper()
		at := now().Add(d)
		if _, err := uc.CreateManual(ctx, tenantID, app.CreateManualInput{Email: email, Reason: domain.ReasonManual, ExpiresAt: &at}); err != nil {
			t.Fatal(err)
		}
	}
	manual(tenant, "ana@example.com", time.Hour)
	manual(tenant, "luis@example.com", 3*time.Hour)
	manual(other, "ana@example.com", time.Hour)
	if _, _, err := uc.Add(ctx, tenant, app.AddInput{Email: "ana@example.com", Reason: domain.ReasonUnsubscribe, Source: "transactional"}); err != nil {
		t.Fatal(err)
	}

	if rep, err := uc.AnnounceExpired(ctx, tenant); err != nil || rep.Announced != 0 {
		t.Fatalf("nada caducado aun: %+v %v", rep, err)
	}

	advance(2 * time.Hour)
	var (
		wg    sync.WaitGroup
		total = make([]int, 2)
		errs  = make([]error, 2)
	)
	for i := range total {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rep, err := replica().AnnounceExpired(ctx, tenant)
			total[i], errs[i] = rep.Announced, err
		}(i)
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || total[0]+total[1] != 1 {
		t.Fatalf("dos replicas anuncian una vez: %v %v", total, errs)
	}
	got := expiredEvents(t, ctx, pool, tenant)
	if len(got) != 1 {
		t.Fatalf("un evento en la outbox: %+v", got)
	}
	want := map[string]interface{}{
		"tenant_id": tenant.String(), "email": "ana@example.com", "reason": "manual", "source": app.SourceAPI,
		"reasons": []interface{}{"unsubscribe"}, "expires_at": "2030-01-01T01:00:00Z",
	}
	if !reflect.DeepEqual(got[0], want) {
		t.Fatalf("payload: %#v", got[0])
	}
	var announced, expires time.Time
	if err := pool.QueryRow(ctx, `SELECT announced_expires_at, expires_at FROM suppression.entries
		WHERE tenant_id = $1 AND email = 'ana@example.com' AND reason = 'manual'`, tenant).Scan(&announced, &expires); err != nil || !announced.Equal(expires) {
		t.Fatalf("la marca guarda la caducidad anunciada: %s / %s %v", announced, expires, err)
	}
	if n := expiredEvents(t, ctx, pool, other); len(n) != 0 {
		t.Fatalf("otra empresa no se barre en esta pasada: %+v", n)
	}
	if rep, err := uc.AnnounceExpired(ctx, tenant); err != nil || rep.Announced != 0 {
		t.Fatalf("otra pasada no repite: %+v %v", rep, err)
	}
	if res, err := uc.Check(ctx, tenant, []string{"ana@example.com"}); err != nil || len(res) != 1 || !reflect.DeepEqual(res[0].Reasons, []domain.Reason{domain.ReasonUnsubscribe}) {
		t.Fatalf("la consulta previa al envio coincide con el anuncio: %+v %v", res, err)
	}

	manual(tenant, "ana@example.com", time.Hour)
	advance(2 * time.Hour)
	if rep, err := uc.AnnounceExpired(ctx, tenant); err != nil || rep.Announced != 2 {
		t.Fatalf("la renovada y la de luis: %+v %v", rep, err)
	}
	got = expiredEvents(t, ctx, pool, tenant)
	var expiries []string
	for _, e := range got {
		expiries = append(expiries, e["email"].(string)+"|"+e["expires_at"].(string))
	}
	if !reflect.DeepEqual(expiries, []string{
		"ana@example.com|2030-01-01T01:00:00Z", "ana@example.com|2030-01-01T03:00:00Z", "luis@example.com|2030-01-01T03:00:00Z",
	}) && !reflect.DeepEqual(expiries, []string{
		"ana@example.com|2030-01-01T01:00:00Z", "luis@example.com|2030-01-01T03:00:00Z", "ana@example.com|2030-01-01T03:00:00Z",
	}) {
		t.Fatalf("anuncios: %v", expiries)
	}
}

// ListExpiryPending recorre por (expires_at, id): con la misma caducidad desempata el id,
// y el cursor no repite ni salta filas. ClaimExpiry reclama una sola vez.
func TestConsultaDeCaducidadesPendientes(t *testing.T) {
	_, ctx := testPool(t)
	repo := NewRepository(&db.ContextPool{})
	tenant := uuid.New()
	lapsed := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	later := lapsed.Add(time.Hour)
	for _, e := range []*domain.Entry{
		{TenantID: tenant, Email: "a@example.com", Reason: domain.ReasonManual, ExpiresAt: &lapsed},
		{TenantID: tenant, Email: "b@example.com", Reason: domain.ReasonManual, ExpiresAt: &lapsed},
		{TenantID: tenant, Email: "c@example.com", Reason: domain.ReasonManual, ExpiresAt: &lapsed},
		{TenantID: tenant, Email: "d@example.com", Reason: domain.ReasonManual, ExpiresAt: &later},
		{TenantID: tenant, Email: "e@example.com", Reason: domain.ReasonManual},
		{TenantID: tenant, Email: "a@example.com", Reason: domain.ReasonHardBounce},
	} {
		if err := repo.Insert(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	now := lapsed.Add(time.Minute)

	var walked []domain.Entry
	var after *ports.ExpiryCursor
	for {
		page, err := repo.ListExpiryPending(ctx, tenant, now, after, 2)
		if err != nil {
			t.Fatal(err)
		}
		walked = append(walked, page...)
		if len(page) < 2 {
			break
		}
		last := page[len(page)-1]
		after = &ports.ExpiryCursor{ExpiresAt: *last.ExpiresAt, ID: last.ID}
	}
	if len(walked) != 3 {
		t.Fatalf("las tres caducadas a esa hora: %+v", walked)
	}
	for i := 1; i < len(walked); i++ {
		if strings.Compare(walked[i-1].ID.String(), walked[i].ID.String()) >= 0 {
			t.Fatalf("en orden de id con la misma caducidad: %s, %s", walked[i-1].ID, walked[i].ID)
		}
	}
	if other, err := repo.ListExpiryPending(ctx, uuid.New(), now, nil, 10); err != nil || len(other) != 0 {
		t.Fatalf("otra empresa: %+v %v", other, err)
	}

	claimed, err := repo.ClaimExpiry(ctx, tenant, walked[0].ID, now)
	if err != nil || claimed.ID != walked[0].ID || claimed.ExpiresAt == nil || !claimed.ExpiresAt.Equal(lapsed) {
		t.Fatalf("reclamo: %+v %v", claimed, err)
	}
	if _, err := repo.ClaimExpiry(ctx, tenant, walked[0].ID, now); err != domain.ErrEntryNotFound {
		t.Fatalf("segundo reclamo: %v", err)
	}
	if _, err := repo.ClaimExpiry(ctx, tenant, walked[1].ID, lapsed.Add(-time.Second)); err != domain.ErrEntryNotFound {
		t.Fatalf("antes de caducar no se reclama: %v", err)
	}
	if left, err := repo.ListExpiryPending(ctx, tenant, now, nil, 10); err != nil || len(left) != 2 {
		t.Fatalf("quedan dos pendientes: %+v %v", left, err)
	}
}
