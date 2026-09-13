//go:build integration

package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/billing/internal/adapters/outbox"
	"github.com/alonsosss/corforce-email/services/billing/internal/app"
	"github.com/alonsosss/corforce-email/services/billing/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

var ctx = context.Background()

// BILLING_TEST_DSN apunta a una base desechable. La prueba le aplica dos veces todas las
// migraciones del registro, donde vive billing (013 y 014, sobre 001, 003 y 004).
func testDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := integrationEnv(t, "BILLING_TEST_DSN")
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	applyRegistryMigrationsTwice(t, dsn)
	return pool
}

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

// applyRegistryMigrationsTwice recorre dos veces el registro completo en orden numerico: la
// segunda pasada demuestra que cada migracion tolera re-ejecutarse. Corre en una conexion
// propia que conserva hasta el final de la prueba el candado asesor que toman tambien
// access-control e identity (otro paquete que migre la misma base mientras esta siembra
// provoca bloqueos mutuos entre el DDL y los INSERT), sin quitarle conexiones al pool.
func applyRegistryMigrationsTwice(t *testing.T, dsn string) {
	t.Helper()
	dir := filepath.Join("..", "..", "..", "..", "..", "migrations", "registry")
	files, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("migraciones del registro en %s: %v", dir, err)
	}
	order := func(path string) int {
		n, convErr := strconv.Atoi(strings.SplitN(filepath.Base(path), "_", 2)[0])
		if convErr != nil {
			t.Fatalf("migracion sin numero: %s", path)
		}
		return n
	}
	sort.Slice(files, func(i, j int) bool { return order(files[i]) < order(files[j]) })

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("conexion de migracion: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtext('registry-migrations-it'))`); err != nil {
		t.Fatalf("candado: %v", err)
	}
	for pass := 1; pass <= 2; pass++ {
		for _, f := range files {
			sql, err := os.ReadFile(f)
			if err != nil {
				t.Fatalf("leer %s: %v", f, err)
			}
			if _, err := conn.Exec(ctx, string(sql)); err != nil {
				t.Fatalf("pasada %d, %s: %v", pass, filepath.Base(f), err)
			}
		}
	}
}

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

func newUseCase(pool *pgxpool.Pool, c *clock, cfg app.Config) *app.UseCase {
	store := NewStore(pool)
	return app.New(app.Deps{
		Plans: NewPlanRepository(store), Subscriptions: NewSubscriptionRepository(store),
		Usage: NewUsageRepository(store), Ledger: NewLedger(store), Tx: store,
		Events: outbox.NewPublisher(store), Config: cfg, Now: c.Now,
	})
}

func uniqueCode() string { return "it-" + uuid.NewString()[:8] }

// planDraft: buzones segun el caso, 3 dominios, 100 mensajes con excedente, resto ilimitado.
func planDraft(code string, mailboxes int64) domain.Plan {
	overage := decimal.RequireFromString("0.001500")
	var limits []domain.PlanLimit
	for _, r := range domain.Resources() {
		l := domain.PlanLimit{Resource: r, Included: domain.Unlimited, HardLimit: true}
		switch r {
		case domain.ResourceMailboxes:
			l.Included = mailboxes
		case domain.ResourceDomains:
			l.Included = 3
		case domain.ResourceTransactionalMessages:
			l.Included, l.HardLimit, l.OverageUnitPrice = 100, false, &overage
		}
		limits = append(limits, l)
	}
	return domain.Plan{
		Code: code, Name: "Plan " + code, Description: "prueba", Currency: "PEN",
		BasePrice: decimal.RequireFromString("49.90"), BillingPeriod: domain.PeriodMonthly, Limits: limits,
	}
}

func outboxData(t *testing.T, pool *pgxpool.Pool, subject string, tenant uuid.UUID) []map[string]interface{} {
	t.Helper()
	rows, err := pool.Query(ctx,
		`SELECT payload FROM platform.event_outbox WHERE subject = $1 AND tenant_id = $2
		  ORDER BY payload->'data'->>'period_start', created_at`, subject, tenant)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]interface{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var env struct {
			Data map[string]interface{} `json:"data"`
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&env); err != nil {
			t.Fatal(err)
		}
		out = append(out, env.Data)
	}
	return out
}

func TestPlanesConLimitesEImportesExactos(t *testing.T) {
	pool := testDB(t)
	uc := newUseCase(pool, &clock{now: time.Now().UTC()}, app.Config{})
	code := uniqueCode()

	p, err := uc.CreatePlan(ctx, planDraft(code, 2))
	if err != nil {
		t.Fatal(err)
	}
	got, err := uc.GetPlan(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.BasePrice.StringFixed(domain.PriceScale) != "49.90" || len(got.Limits) != len(domain.Resources()) ||
		got.Limits[0].Resource != domain.ResourceUsers {
		t.Fatalf("plan leido: %+v", got)
	}
	tm := got.EffectiveLimit(domain.ResourceTransactionalMessages)
	if tm.OverageUnitPrice == nil || tm.OverageUnitPrice.StringFixed(domain.UnitPriceScale) != "0.001500" || tm.HardLimit {
		t.Fatalf("limite con excedente: %+v", tm)
	}
	var stored string
	if err := pool.QueryRow(ctx, `SELECT base_price::text FROM billing.plans WHERE id = $1`, p.ID).Scan(&stored); err != nil || stored != "49.90" {
		t.Fatalf("numeric guardado: %q %v", stored, err)
	}
	if _, err := uc.CreatePlan(ctx, planDraft(code, 2)); !errors.Is(err, domain.ErrPlanCodeTaken) {
		t.Fatalf("codigo repetido: %v", err)
	}
	// La base defiende el estado imposible aunque el codigo se equivoque.
	if _, err := pool.Exec(ctx,
		`UPDATE billing.plan_limits SET overage_unit_price = 1 WHERE plan_id = $1 AND resource = 'mailboxes'`, p.ID); err == nil {
		t.Fatal("excedente en un limite duro debe violar el CHECK")
	}

	if _, err := uc.RetirePlan(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	active, err := uc.ListPlans(ctx, domain.PlanActive)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range active {
		if a.ID == p.ID {
			t.Fatal("un plan retirado no aparece entre los activos")
		}
	}
	if _, _, err := uc.PutSubscription(ctx, uuid.New(), app.PutSubscriptionInput{PlanCode: code}); !errors.Is(err, domain.ErrPlanRetired) {
		t.Fatalf("un plan retirado no admite empresas nuevas: %v", err)
	}
}

func TestConsumoIdempotenteYDerechoDenegadoEnElLimite(t *testing.T) {
	pool := testDB(t)
	uc := newUseCase(pool, &clock{now: time.Now().UTC()}, app.Config{})
	code := uniqueCode()
	if _, err := uc.CreatePlan(ctx, planDraft(code, 2)); err != nil {
		t.Fatal(err)
	}
	tenant := uuid.New()
	if _, created, err := uc.PutSubscription(ctx, tenant, app.PutSubscriptionInput{PlanCode: code}); err != nil || !created {
		t.Fatalf("suscripcion: %v %v", created, err)
	}
	if n := len(outboxData(t, pool, "billing.subscription.created", tenant)); n != 1 {
		t.Fatalf("billing.subscription.created en la outbox: %d", n)
	}

	mailbox := func(delta int64) domain.UsageChange {
		subject := "mail.mailbox.created"
		if delta < 0 {
			subject = "mail.mailbox.deleted"
		}
		return domain.UsageChange{EventID: uuid.NewString(), Subject: subject, TenantID: tenant,
			Resource: domain.ResourceMailboxes, Delta: delta}
	}
	first := mailbox(1)
	if r, err := uc.RecordUsage(ctx, first); err != nil || r.Quantity != 1 {
		t.Fatalf("primer buzon: %+v %v", r, err)
	}
	if r, err := uc.RecordUsage(ctx, first); err != nil || !r.Duplicate {
		t.Fatalf("la reentrega no cuenta: %+v %v", r, err)
	}
	ent, err := uc.CheckEntitlement(ctx, tenant, domain.ResourceMailboxes, 1)
	if err != nil || !ent.Allowed || *ent.Remaining != 1 {
		t.Fatalf("con margen: %+v %v", ent, err)
	}
	if r, err := uc.RecordUsage(ctx, mailbox(1)); err != nil || r.Quantity != 2 {
		t.Fatalf("segundo buzon: %+v %v", r, err)
	}
	ent, err = uc.CheckEntitlement(ctx, tenant, domain.ResourceMailboxes, 1)
	if err != nil || ent.Allowed || ent.Reason != domain.ReasonLimitReached || ent.Used != 2 {
		t.Fatalf("en el tope se deniega: %+v %v", ent, err)
	}
	// Un alta que llego igual (sin consultar) se cuenta, pero no repite el aviso.
	if _, err := uc.RecordUsage(ctx, mailbox(1)); err != nil {
		t.Fatal(err)
	}
	reached := outboxData(t, pool, "billing.limit.reached", tenant)
	if len(reached) != 1 || reached[0]["resource"] != "mailboxes" || fmt.Sprint(reached[0]["limit"]) != "2" {
		t.Fatalf("billing.limit.reached una vez: %+v", reached)
	}
	for i := 0; i < 5; i++ {
		if _, err := uc.RecordUsage(ctx, mailbox(-1)); err != nil {
			t.Fatal(err)
		}
	}
	q, err := NewUsageRepository(NewStore(pool)).Quantity(ctx, tenant, domain.ResourceMailboxes, domain.StockPeriodStart)
	if err != nil || q != 0 {
		t.Fatalf("el stock no baja de cero: %d %v", q, err)
	}
}

func TestDominioDeDosFuentesConcurrentesCuentaUnaVez(t *testing.T) {
	pool := testDB(t)
	uc := newUseCase(pool, &clock{now: time.Now().UTC()}, app.Config{})
	tenant := uuid.New()
	const domains = 10
	for i := 0; i < domains; i++ {
		key := fmt.Sprintf("d%d.empresa.test", i)
		var wg sync.WaitGroup
		errs := make(chan error, 2)
		for _, src := range []string{"domain-service", "mail-directory"} {
			wg.Add(1)
			go func(src string) {
				defer wg.Done()
				_, err := uc.RecordUsage(ctx, domain.UsageChange{EventID: uuid.NewString(), Subject: "domain.created",
					TenantID: tenant, Resource: domain.ResourceDomains, Delta: 1, ItemKey: key, Source: src})
				errs <- err
			}(src)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	repo := NewUsageRepository(NewStore(pool))
	if q, err := repo.Quantity(ctx, tenant, domain.ResourceDomains, domain.StockPeriodStart); err != nil || q != domains {
		t.Fatalf("dominios contados: %d %v; se esperaba %d", q, err, domains)
	}
	if _, err := uc.RecordUsage(ctx, domain.UsageChange{EventID: uuid.NewString(), Subject: "domain.deleted",
		TenantID: tenant, Resource: domain.ResourceDomains, Delta: -1, ItemKey: "d0.empresa.test", Source: "mail-directory"}); err != nil {
		t.Fatal(err)
	}
	if q, _ := repo.Quantity(ctx, tenant, domain.ResourceDomains, domain.StockPeriodStart); q != domains {
		t.Fatalf("mientras quede una fuente el dominio sigue contando: %d", q)
	}
}

func TestCierreDePeriodoPublicaElConsumo(t *testing.T) {
	pool := testDB(t)
	c := &clock{now: time.Date(2026, 1, 31, 12, 0, 0, 0, time.UTC)}
	uc := newUseCase(pool, c, app.Config{})
	code := uniqueCode()
	if _, err := uc.CreatePlan(ctx, planDraft(code, 5)); err != nil {
		t.Fatal(err)
	}
	tenant := uuid.New()
	if _, _, err := uc.PutSubscription(ctx, tenant, app.PutSubscriptionInput{PlanCode: code}); err != nil {
		t.Fatal(err)
	}
	// Cada envio suma sus destinatarios, como lo traduce el consumidor de transactional.email.sent.
	send := func(recipients int64) {
		t.Helper()
		if _, err := uc.RecordUsage(ctx, domain.UsageChange{EventID: uuid.NewString(), Subject: "transactional.email.sent",
			TenantID: tenant, Resource: domain.ResourceTransactionalMessages, Delta: recipients}); err != nil {
			t.Fatal(err)
		}
	}
	c.Set(time.Date(2026, 2, 10, 9, 0, 0, 0, time.UTC))
	send(1)
	c.Set(time.Date(2026, 3, 5, 9, 0, 0, 0, time.UTC))
	send(2)
	send(1)

	c.Set(time.Date(2026, 4, 2, 6, 0, 0, 0, time.UTC))
	if rep, err := uc.SweepPeriods(ctx); err != nil || rep.Failed != 0 || rep.Closed < 2 {
		t.Fatalf("barrido: %+v %v", rep, err)
	}
	closed := outboxData(t, pool, "billing.period.closed", tenant)
	if len(closed) != 2 {
		t.Fatalf("un cierre por periodo vencido: %d", len(closed))
	}
	want := []struct{ start, end, sent string }{{"2026-01-31", "2026-02-28", "1"}, {"2026-02-28", "2026-03-31", "3"}}
	for i, w := range want {
		usage, _ := closed[i]["usage"].(map[string]interface{})
		if closed[i]["period_start"] != w.start || closed[i]["period_end"] != w.end ||
			fmt.Sprint(usage["transactional_messages"]) != w.sent || len(usage) != len(domain.Resources()) {
			t.Fatalf("cierre %d: %+v", i, closed[i])
		}
	}
	sub, _, err := uc.GetSubscription(ctx, tenant)
	if err != nil || sub.CurrentPeriodStart.Format("2006-01-02") != "2026-03-31" || sub.CurrentPeriodEnd.Format("2006-01-02") != "2026-04-30" {
		t.Fatalf("periodo vigente: %+v %v", sub, err)
	}
	if _, err := uc.SweepPeriods(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(outboxData(t, pool, "billing.period.closed", tenant)); n != 2 {
		t.Fatalf("un segundo barrido no vuelve a cerrar: %d", n)
	}
}

func TestPodaDelRastroDeDeduplicacion(t *testing.T) {
	pool := testDB(t)
	ledger := NewLedger(NewStore(pool))
	id := uuid.NewString()
	if fresh, err := ledger.MarkProcessed(ctx, id, "x.y.z"); err != nil || !fresh {
		t.Fatalf("primera marca: %v %v", fresh, err)
	}
	if fresh, _ := ledger.MarkProcessed(ctx, id, "x.y.z"); fresh {
		t.Fatal("la segunda marca debe detectarse")
	}
	if n, err := ledger.PruneProcessed(ctx, time.Now().Add(time.Hour)); err != nil || n < 1 {
		t.Fatalf("poda: %d %v", n, err)
	}
	if fresh, _ := ledger.MarkProcessed(ctx, id, "x.y.z"); !fresh {
		t.Fatal("tras la poda el id vuelve a estar libre")
	}
}
