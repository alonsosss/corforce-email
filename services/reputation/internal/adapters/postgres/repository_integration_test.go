//go:build integration

package postgres

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/reputation/internal/domain"
	"github.com/alonsosss/corforce-email/services/reputation/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

// repoRoot es la raiz del repositorio vista desde este paquete.
var repoRoot = filepath.Join("..", "..", "..", "..", "..")

var migrations = []string{
	"migrations/tenant/canonical/platform/00_outbox.sql",
	"migrations/tenant/canonical/reputation/01_reputation.sql",
}

var (
	migrateOnce sync.Once
	migrateErr  error
)

// integrationEnv devuelve la variable de entorno que apunta a la infraestructura de la
// prueba. Sin ella la prueba se salta, salvo con INTEGRATION_REQUIRED=1 (make
// test-integration y CI): ahi es un fallo, porque un salto esconderia que no llego.
func integrationEnv(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		if os.Getenv("INTEGRATION_REQUIRED") == "1" {
			t.Fatalf("%s no definida con INTEGRATION_REQUIRED=1", name)
		}
		t.Skipf("%s no definida", name)
	}
	return v
}

// testDB abre REPUTATION_TEST_DSN (una base desechable) y aplica la outbox y la migracion
// del servicio DOS veces: la segunda pasada demuestra que son idempotentes.
func testDB(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	dsn := integrationEnv(t, "REPUTATION_TEST_DSN")
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	migrateOnce.Do(func() {
		for pass := 0; pass < 2 && migrateErr == nil; pass++ {
			for _, m := range migrations {
				sql, err := os.ReadFile(filepath.Join(repoRoot, m))
				if err != nil {
					migrateErr = err
					return
				}
				if _, err := pool.Exec(ctx, string(sql)); err != nil {
					migrateErr = errors.Join(errors.New(m), err)
					return
				}
			}
		}
	})
	if migrateErr != nil {
		t.Fatalf("migraciones: %v", migrateErr)
	}
	return pool, db.WithPool(ctx, pool)
}

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func TestEstadisticasYDeduplicacion(t *testing.T) {
	_, ctx := testDB(t)
	repo := NewStatsRepository(&db.ContextPool{})
	tenant := uuid.New()
	eventID := uuid.NewString()

	if fresh, err := repo.MarkProcessed(ctx, tenant, eventID); err != nil || !fresh {
		t.Fatalf("primera marca: %v %v", fresh, err)
	}
	if fresh, err := repo.MarkProcessed(ctx, tenant, eventID); err != nil || fresh {
		t.Fatalf("la segunda marca del mismo evento debe decir duplicado: %v %v", fresh, err)
	}

	for _, add := range []struct {
		class domain.Class
		day   time.Time
		c     domain.Counts
	}{
		{domain.ClassMarketing, day(2026, 9, 12), domain.Counts{Sent: 100}},
		{domain.ClassMarketing, day(2026, 9, 12), domain.Counts{Sent: 50, Bounced: 3}},
		{domain.ClassMarketing, day(2026, 9, 5), domain.Counts{Sent: 1000, Bounced: 900}},
		{domain.ClassTransactional, day(2026, 9, 11), domain.Counts{Sent: 20, Complained: 1}},
	} {
		if err := repo.AddDaily(ctx, tenant, add.class, add.day, add.c); err != nil {
			t.Fatal(err)
		}
	}
	c, err := repo.WindowCounts(ctx, tenant, domain.ClassMarketing, day(2026, 9, 6))
	if err != nil || c != (domain.Counts{Sent: 150, Bounced: 3}) {
		t.Fatalf("ventana de marketing: %+v %v", c, err)
	}
	if c, err := repo.WindowCounts(ctx, uuid.New(), domain.ClassMarketing, day(2026, 1, 1)); err != nil || c != (domain.Counts{}) {
		t.Fatalf("sin filas la ventana suma cero: %+v %v", c, err)
	}
	byClass, err := repo.WindowCountsByClass(ctx, tenant, day(2026, 9, 6))
	if err != nil || byClass[domain.ClassTransactional] != (domain.Counts{Sent: 20, Complained: 1}) || byClass[domain.ClassMarketing].Sent != 150 {
		t.Fatalf("por clase: %+v %v", byClass, err)
	}

	events, days, err := repo.Prune(ctx, time.Now().Add(time.Minute), day(2026, 9, 6))
	if err != nil || events < 1 || days < 1 {
		t.Fatalf("poda: eventos=%d dias=%d err=%v", events, days, err)
	}
	if fresh, err := repo.MarkProcessed(ctx, tenant, eventID); err != nil || !fresh {
		t.Fatalf("un evento podado se puede volver a anotar: %v %v", fresh, err)
	}
	if c, _ := repo.WindowCounts(ctx, tenant, domain.ClassMarketing, day(2026, 1, 1)); c.Sent != 150 {
		t.Fatalf("la poda solo borra los dias viejos: %+v", c)
	}
}

func TestEstadosEHistorial(t *testing.T) {
	pool, ctx := testDB(t)
	cp := &db.ContextPool{}
	repo := NewStateRepository(cp)
	tenant := uuid.New()
	now := time.Now().UTC().Truncate(time.Microsecond)

	rec, err := repo.Get(ctx, tenant, domain.ClassMarketing)
	if err != nil || rec.State != domain.StateOK || rec.Reason != domain.ReasonInitial {
		t.Fatalf("sin fila el estado es el inicial: %+v %v", rec, err)
	}
	for i := 0; i < 2; i++ {
		if err := repo.Ensure(ctx, tenant, domain.ClassMarketing, now); err != nil {
			t.Fatal(err)
		}
	}

	by := uuid.New()
	err = cp.Transact(ctx, func(ctx context.Context) error {
		cur, err := repo.GetForUpdate(ctx, tenant, domain.ClassMarketing)
		if err != nil {
			return err
		}
		cur.State, cur.Reason, cur.Manual, cur.ChangedBy = domain.StateSuspended, "revision", true, &by
		cur.BounceRate, cur.ComplaintRate = decimal.RequireFromString("0.041234"), decimal.RequireFromString("0.0008")
		return repo.Save(ctx, cur)
	})
	if err != nil {
		t.Fatal(err)
	}
	rec, err = repo.Get(ctx, tenant, domain.ClassMarketing)
	if err != nil || rec.State != domain.StateSuspended || !rec.Manual || *rec.ChangedBy != by {
		t.Fatalf("guardado: %+v %v", rec, err)
	}
	if rec.BounceRate.StringFixed(domain.RateScale) != "0.041234" || !rec.ComplaintRate.Equal(decimal.RequireFromString("0.0008")) {
		t.Fatalf("las tasas vuelven exactas: %s %s", rec.BounceRate, rec.ComplaintRate)
	}

	bad := rec
	bad.Manual = false
	if err := repo.Save(ctx, bad); err == nil {
		t.Fatal("suspended solo existe como estado manual (CHECK)")
	}
	bad = rec
	bad.BounceRate = decimal.RequireFromString("1.5")
	if err := repo.Save(ctx, bad); err == nil {
		t.Fatal("una tasa por encima de 1 viola el CHECK")
	}
	if err := repo.Save(ctx, domain.Record{TenantID: uuid.New(), Class: domain.ClassMarketing, State: domain.StateOK}); err == nil {
		t.Fatal("guardar sin fila debe fallar")
	}

	list, err := repo.List(ctx, tenant)
	if err != nil || len(list) != 1 {
		t.Fatalf("listado: %+v %v", list, err)
	}

	for i, to := range []domain.State{domain.StateWarning, domain.StateSuspended} {
		c := &domain.Change{TenantID: tenant, Class: domain.ClassMarketing, From: domain.StateOK, To: to,
			Reason: "r", BounceRate: decimal.RequireFromString("0.02"), ComplaintRate: decimal.Zero,
			Manual: i == 1, ChangedBy: &by, CreatedAt: now.Add(time.Duration(i) * time.Second)}
		if err := repo.AppendHistory(ctx, c); err != nil {
			t.Fatal(err)
		}
		if c.ID == uuid.Nil {
			t.Fatal("el historial debe devolver el id")
		}
	}
	if err := repo.AppendHistory(ctx, &domain.Change{TenantID: tenant, Class: domain.ClassTransactional, From: domain.StateOK, To: domain.StateWarning}); err != nil {
		t.Fatal(err)
	}
	items, total, err := repo.ListHistory(ctx, tenant, ports.HistoryFilter{Class: domain.ClassMarketing, Page: 1, PerPage: 1})
	if err != nil || total != 2 || len(items) != 1 || items[0].To != domain.StateSuspended || !items[0].Manual {
		t.Fatalf("historial filtrado y paginado: %+v total=%d err=%v", items, total, err)
	}
	if _, total, err := repo.ListHistory(ctx, tenant, ports.HistoryFilter{Page: 1, PerPage: 10}); err != nil || total != 3 {
		t.Fatalf("historial completo: total=%d err=%v", total, err)
	}

	if _, err := pool.Exec(ctx, `UPDATE reputation.state_history SET reason = 'x' WHERE tenant_id = $1`, tenant); err == nil {
		t.Fatal("el historial no se puede modificar")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM reputation.state_history WHERE tenant_id = $1`, tenant); err == nil {
		t.Fatal("el historial no se puede borrar")
	}
}

func TestLimitesPropios(t *testing.T) {
	_, ctx := testDB(t)
	repo := NewLimitRepository(&db.ContextPool{})
	tenant, by := uuid.New(), uuid.New()
	v := func(n int64) *int64 { return &n }

	if o, err := repo.Get(ctx, tenant, domain.ClassMarketing); err != nil || o != nil {
		t.Fatalf("sin limites propios: %+v %v", o, err)
	}
	o := &domain.LimitOverride{TenantID: tenant, Class: domain.ClassMarketing, Hourly: v(10), UpdatedBy: by}
	if err := repo.Upsert(ctx, o); err != nil || o.UpdatedAt.IsZero() {
		t.Fatalf("alta: %v", err)
	}
	o.Daily = v(100)
	if err := repo.Upsert(ctx, o); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, tenant, domain.ClassMarketing)
	if err != nil || got == nil || *got.Hourly != 10 || *got.Daily != 100 || got.UpdatedBy != by {
		t.Fatalf("reemplazo: %+v %v", got, err)
	}
	if err := repo.Upsert(ctx, &domain.LimitOverride{TenantID: tenant, Class: domain.ClassTransactional, Hourly: v(50), Daily: v(5), UpdatedBy: by}); err == nil {
		t.Fatal("hourly mayor que daily viola el CHECK")
	}
	if list, err := repo.List(ctx, tenant); err != nil || len(list) != 1 {
		t.Fatalf("listado: %+v %v", list, err)
	}
	if err := repo.Delete(ctx, tenant, domain.ClassMarketing); err != nil {
		t.Fatal(err)
	}
	if o, err := repo.Get(ctx, tenant, domain.ClassMarketing); err != nil || o != nil {
		t.Fatalf("tras borrar: %+v %v", o, err)
	}
}
