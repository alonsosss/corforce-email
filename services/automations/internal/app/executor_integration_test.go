//go:build integration

// Pruebas del ejecutor y del doble opt-in contra un Postgres real, con transactional,
// contacts y templates sustituidos por los dobles de apptest:
//
//	AUTOMATIONS_TEST_DSN=postgres://... go test -tags integration ./services/automations/...
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
	"github.com/alonsosss/corforce-email/services/automations/internal/adapters/postgres"
	"github.com/alonsosss/corforce-email/services/automations/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/automations/internal/domain"
	"github.com/alonsosss/corforce-email/services/automations/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

var errCrash = errors.New("caida simulada entre la respuesta de transactional y el commit")

// crashingRuns hace fallar el avance como si el proceso muriera antes del commit.
type crashingRuns struct {
	ports.RunRepository
	fail atomic.Int32
}

func (c *crashingRuns) Advance(ctx context.Context, r *domain.Run, next int, at time.Time) (bool, error) {
	if c.fail.Add(-1) >= 0 {
		return false, errCrash
	}
	return c.RunRepository.Advance(ctx, r, next, at)
}

var migrateOnce sync.Once

type dbFixture struct {
	*fixture
	pool *pgxpool.Pool
	runs *crashingRuns
	ctx  context.Context
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

func setupDB(t *testing.T, cfg Config) *dbFixture {
	t.Helper()
	dsn := integrationEnv(t, "AUTOMATIONS_TEST_DSN")
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
			"migrations/tenant/canonical/automations/01_automations.sql",
			"migrations/tenant/canonical/automations/03_branches_and_dates.sql",
			"migrations/tenant/canonical/platform/00_outbox.sql",
			"migrations/tenant/canonical/automations/01_automations.sql",
			"migrations/tenant/canonical/automations/03_branches_and_dates.sql",
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

	clock := &apptest.Clock{T: time.Now().UTC().Truncate(time.Microsecond)}
	f := &fixture{
		clock: clock, sender: &apptest.Sender{}, contacts: apptest.NewContacts(),
		templates: &apptest.Templates{Kind: KindMarketing, Version: 3}, tenant: uuid.New(), user: uuid.New(),
		rules: apptest.NewRules(),
	}
	if cfg.PublicBaseURL == "" {
		cfg.PublicBaseURL = "https://app.example.com"
	}
	ctxPool := &db.ContextPool{}
	runs := &crashingRuns{RunRepository: postgres.NewRunRepository(ctxPool)}
	f.uc = New(Deps{
		Settings: postgres.NewSettingsRepository(ctxPool), Deliveries: postgres.NewDeliveryRepository(ctxPool),
		Workflows: postgres.NewWorkflowRepository(ctxPool), Runs: runs, Processed: postgres.NewProcessedRepository(ctxPool),
		Tx: ctxPool, Events: postgres.NewOutboxPublisher(ctxPool), Sender: f.sender, Contacts: f.contacts,
		Templates: f.templates, Rules: f.rules, RunMessages: postgres.NewRunMessageRepository(ctxPool),
		DateScans: postgres.NewDateScanRepository(ctxPool), Config: cfg, Now: clock.Now,
	})
	return &dbFixture{fixture: f, pool: pool, runs: runs, ctx: db.WithTenant(context.Background(), pool, f.tenant.String())}
}

func (d *dbFixture) activate(t *testing.T, reEntry bool, steps ...domain.Step) *domain.Workflow {
	t.Helper()
	w, err := d.uc.CreateWorkflow(d.ctx, d.tenant, domain.NewWorkflowInput{
		Name: "Flujo " + uuid.NewString()[:8], Trigger: contactCreated, ReEntry: reEntry, Steps: steps, CreatedBy: d.user,
	})
	if err != nil {
		t.Fatal(err)
	}
	if w, err = d.uc.ActivateWorkflow(d.ctx, d.tenant, w.ID); err != nil {
		t.Fatal(err)
	}
	return w
}

func (d *dbFixture) runsOf(t *testing.T, workflowID uuid.UUID) []domain.Run {
	t.Helper()
	list, _, err := postgres.NewRunRepository(&db.ContextPool{}).List(d.ctx, d.tenant, ports.RunFilter{WorkflowID: workflowID, Page: 1, PerPage: 100})
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func TestIntegracionSkipLockedDosTrabajadoresSobreLaMismaEjecucion(t *testing.T) {
	d := setupDB(t, Config{})
	w := d.activate(t, false, sendEmail())
	if n, err := d.uc.HandleTrigger(d.ctx, domain.TriggerEvent{
		EventID: uuid.NewString(), TenantID: d.tenant, Type: domain.TriggerContactCreated, ContactID: uuid.New(),
	}); err != nil || n != 1 {
		t.Fatalf("entrada: %d %v", n, err)
	}
	run := d.runsOf(t, w.ID)[0]
	repo := postgres.NewRunRepository(&db.ContextPool{})
	for round := 0; round < 25; round++ {
		if _, err := d.pool.Exec(context.Background(),
			`UPDATE automations.runs SET status = 'waiting', lease_token = NULL, lease_until = NULL WHERE id = $1`, run.ID); err != nil {
			t.Fatal(err)
		}
		var (
			wg      sync.WaitGroup
			start   = make(chan struct{})
			claimed atomic.Int32
			tokens  sync.Map
		)
		now := d.clock.Now()
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				got, err := repo.ClaimDue(d.ctx, d.tenant, now, now.Add(domain.RunLease), 10)
				if err != nil {
					t.Error(err)
					return
				}
				for _, r := range got {
					claimed.Add(1)
					tokens.Store(*r.LeaseToken, true)
				}
			}()
		}
		close(start)
		wg.Wait()
		if claimed.Load() != 1 {
			t.Fatalf("ronda %d: la ejecucion la reserva un solo trabajador, la reservaron %d", round, claimed.Load())
		}
	}
}

func TestIntegracionUnicidadDeEjecucionesPorEvento(t *testing.T) {
	d := setupDB(t, Config{})
	once := d.activate(t, false, sendEmail())
	again := d.activate(t, true, sendEmail())
	repo := postgres.NewRunRepository(&db.ContextPool{})
	contact := uuid.New()
	ev := domain.TriggerEvent{EventID: uuid.NewString(), TenantID: d.tenant, Type: domain.TriggerContactCreated, ContactID: contact}
	now := d.clock.Now()

	for _, w := range []*domain.Workflow{once, again} {
		if ok, err := repo.Enroll(d.ctx, domain.NewRun(w, ev, now), w.ReEntry); err != nil || !ok {
			t.Fatalf("primera entrada: %v %v", ok, err)
		}
		if ok, err := repo.Enroll(d.ctx, domain.NewRun(w, ev, now), w.ReEntry); err != nil || ok {
			t.Fatalf("el mismo evento no crea otra ejecucion: %v %v", ok, err)
		}
	}
	ev2 := ev
	ev2.EventID = uuid.NewString()
	if ok, _ := repo.Enroll(d.ctx, domain.NewRun(once, ev2, now), false); ok {
		t.Fatal("sin reentrada, otro evento del mismo contacto no entra")
	}
	if ok, _ := repo.Enroll(d.ctx, domain.NewRun(again, ev2, now), true); !ok {
		t.Fatal("con reentrada, otro evento entra")
	}
	// Sin reentrada no entra quien ya recorrio el flujo con otra clave (reentrada desactivada despues).
	ev3 := ev
	ev3.EventID = uuid.NewString()
	if ok, _ := repo.Enroll(d.ctx, domain.NewRun(&domain.Workflow{ID: again.ID, TenantID: d.tenant}, ev3, now), false); ok {
		t.Fatal("el contacto ya recorrio el flujo")
	}

	// Dos eventos simultaneos del mismo contacto nuevo, sin reentrada: entra uno.
	fresh := uuid.New()
	var wg sync.WaitGroup
	var inserted atomic.Int32
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e := domain.TriggerEvent{EventID: uuid.NewString(), TenantID: d.tenant, Type: domain.TriggerContactCreated, ContactID: fresh}
			ok, err := repo.Enroll(d.ctx, domain.NewRun(once, e, now), false)
			if err != nil {
				t.Error(err)
			}
			if ok {
				inserted.Add(1)
			}
		}()
	}
	wg.Wait()
	if inserted.Load() != 1 {
		t.Fatalf("en concurrencia entra una sola vez: %d", inserted.Load())
	}
}

func TestIntegracionReanudacionTrasCaidaConLaMismaClave(t *testing.T) {
	d := setupDB(t, Config{})
	c := d.sendable("ana@example.com")
	w := d.activate(t, false, sendEmail(), domain.Step{Type: domain.StepWait, Duration: "1d"})
	if _, err := d.uc.HandleTrigger(d.ctx, domain.TriggerEvent{
		EventID: uuid.NewString(), TenantID: d.tenant, Type: domain.TriggerContactCreated, ContactID: c.ID,
	}); err != nil {
		t.Fatal(err)
	}

	d.runs.fail.Store(1)
	if err := d.uc.Tick(d.ctx, d.tenant); err != nil {
		t.Fatal(err)
	}
	run := d.runsOf(t, w.ID)[0]
	if run.Status != domain.RunRunning || run.StepIndex != 0 || len(d.sender.MarketingCalls) != 1 {
		t.Fatalf("tras la caida sigue reservada en el paso 0: %+v", run)
	}
	if err := d.uc.Tick(d.ctx, d.tenant); err != nil {
		t.Fatal(err)
	}
	if len(d.sender.MarketingCalls) != 1 {
		t.Fatal("con la reserva viva nadie repite el paso")
	}
	d.clock.Advance(domain.RunLease + time.Second)
	if err := d.uc.Tick(d.ctx, d.tenant); err != nil {
		t.Fatal(err)
	}
	if len(d.sender.MarketingCalls) != 2 || d.sender.Created() != 1 ||
		d.sender.MarketingCalls[0].IdempotencyKey != d.sender.MarketingCalls[1].IdempotencyKey {
		t.Fatalf("reintento con la misma clave y un solo mensaje: %d llamadas, %d mensajes", len(d.sender.MarketingCalls), d.sender.Created())
	}
	run = d.runsOf(t, w.ID)[0]
	if run.StepIndex != 1 || run.Status != domain.RunWaiting || run.LeaseToken != nil {
		t.Fatalf("avanza una sola vez: %+v", run)
	}
	// La espera empieza al llegar a ella: este tick la fija a un dia.
	if err := d.uc.Tick(d.ctx, d.tenant); err != nil {
		t.Fatal(err)
	}
	if run = d.runsOf(t, w.ID)[0]; run.StepIndex != 2 || !run.NextRunAt.Equal(d.clock.Now().Add(24*time.Hour)) {
		t.Fatalf("espera de un dia: %+v", run)
	}
	d.clock.Advance(24*time.Hour + time.Second)
	if err := d.uc.Tick(d.ctx, d.tenant); err != nil {
		t.Fatal(err)
	}
	if run = d.runsOf(t, w.ID)[0]; run.Status != domain.RunCompleted || run.FinishedAt == nil {
		t.Fatalf("termina tras la espera: %+v", run)
	}
	var pending int
	if err := d.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM platform.event_outbox WHERE tenant_id = $1 AND subject = 'automations.run.completed'`, d.tenant).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 1 {
		t.Fatalf("el evento de fin sale por la outbox: %d", pending)
	}
}

// Dos eventos simultaneos del mismo contacto con un correo al dia: el bloqueo por contacto
// hace que el segundo vea el primero y quede omitido.
func TestIntegracionLimiteDelDobleOptInEnConcurrencia(t *testing.T) {
	d := setupDB(t, Config{DOILimits: domain.DOILimits{PerDay: 1, Per30Days: 3}})
	tpl := uuid.New()
	if err := postgres.NewSettingsRepository(&db.ContextPool{}).UpsertDOISettings(d.ctx, &domain.DOISettings{
		TenantID: d.tenant, Enabled: true, TemplateID: &tpl, FromEmail: "hola@shop.example.com",
	}); err != nil {
		t.Fatal(err)
	}
	contact := uuid.New()
	var wg sync.WaitGroup
	statuses := make(chan domain.DOIStatus, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, err := d.uc.HandleConsentRequested(d.ctx, domain.ConsentRequest{
				EventID: uuid.NewString(), TenantID: d.tenant, ContactID: contact,
				Email: "ana@example.com", ConfirmURL: "https://app.example.com/c?t=x&k=y",
			})
			if err != nil {
				t.Error(err)
			}
			statuses <- status
		}()
	}
	wg.Wait()
	close(statuses)
	sent := 0
	for s := range statuses {
		if s == domain.DOISent {
			sent++
		}
	}
	if sent != 1 || d.sender.Created() != 1 {
		t.Fatalf("un solo correo de confirmacion: %d enviados, %d mensajes", sent, d.sender.Created())
	}
	list, total, err := postgres.NewDeliveryRepository(&db.ContextPool{}).List(d.ctx, d.tenant, domain.DOISkipped, 1, 10)
	if err != nil || total != 3 || list[0].Reason != domain.ReasonRateLimited {
		t.Fatalf("los demas quedan omitidos por el limite: %v %d", err, total)
	}
}
