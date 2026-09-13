//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/scheduler/internal/domain"
	"github.com/google/uuid"
)

func (c *testClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

func TestMigracionDeLaZonaDelTrabajo(t *testing.T) {
	e := setup(t)
	var nullable, def string
	var width int
	if err := e.pool.QueryRow(context.Background(),
		`SELECT is_nullable, column_default, character_maximum_length FROM information_schema.columns
		  WHERE table_schema = 'scheduler' AND table_name = 'job_definitions' AND column_name = 'timezone'`).Scan(&nullable, &def, &width); err != nil {
		t.Fatalf("columna timezone: %v", err)
	}
	if nullable != "NO" || def != "'UTC'::character varying" || width != domain.MaxTimezoneLength {
		t.Fatalf("timezone: nullable %s, default %s, ancho %d", nullable, def, width)
	}
	if n := e.count(t, `SELECT count(*) FROM pg_constraint WHERE conname = 'job_definitions_timezone_check'`); n != 1 {
		t.Fatalf("restriccion de la zona: %d", n)
	}

	// Una fila escrita sin zona (como las anteriores a la migracion) queda en UTC, y el
	// repositorio la lee asi.
	daily := "0 3 * * *"
	legacy := e.insertLegacyJob(t, domain.JobTypeCron, &daily, nil, e.clock.now())
	job, err := e.uc.GetJob(e.ctx, legacy, e.tenant)
	if err != nil || job.Timezone != domain.DefaultTimezone {
		t.Fatalf("trabajo previo: %+v (%v)", job, err)
	}
	if _, err := e.pool.Exec(context.Background(), `UPDATE scheduler.job_definitions SET timezone = '' WHERE id = $1`, legacy); err == nil {
		t.Fatal("la base acepta una zona vacia")
	}
}

func TestCronEnLaZonaDelTrabajoContraLaBase(t *testing.T) {
	e := setup(t)
	// 2026-11-01 00:00 EDT; a las 06:00 UTC Nueva York atrasa el reloj de 02:00 EDT a 01:00 EST.
	e.clock.set(time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC))
	tenant, expr := e.tenant, "30 1 * * *"
	job := &domain.JobDefinition{TenantID: &tenant, Name: "Cierre", Code: "it-" + uuid.NewString(), JobType: domain.JobTypeCron,
		CronExpression: &expr, Timezone: "America/New_York", Handler: "it.report", TimeoutSeconds: 30}
	if err := e.uc.CreateJob(e.ctx, job); err != nil {
		t.Fatal(err)
	}
	if stored, err := e.uc.GetJob(e.ctx, job.ID, e.tenant); err != nil || stored.Timezone != "America/New_York" {
		t.Fatalf("zona guardada: %+v (%v)", stored, err)
	}
	if next, _ := e.schedule(t, job.ID); !next.Equal(time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC)) {
		t.Fatalf("alta: next_run_at %v, se esperaba 01:30 EDT", next)
	}

	// Pasadas del ticker a lo largo de la hora repetida: 01:30 EDT, 01:00 EST, 01:30 EST.
	for _, tick := range []time.Time{
		time.Date(2026, 11, 1, 5, 30, 10, 0, time.UTC),
		time.Date(2026, 11, 1, 6, 0, 10, 0, time.UTC),
		time.Date(2026, 11, 1, 6, 30, 10, 0, time.UTC),
	} {
		e.clock.set(tick)
		e.uc.ProcessDueJobs(e.ctx)
	}
	if n := e.count(t, `SELECT count(*) FROM scheduler.job_executions WHERE job_id = $1`, job.ID); n != 1 {
		t.Fatalf("la hora repetida lanzo %d ejecuciones, se esperaba 1", n)
	}
	if next, _ := e.schedule(t, job.ID); !next.Equal(time.Date(2026, 11, 2, 6, 30, 0, 0, time.UTC)) {
		t.Fatalf("siguiente: %v, se esperaba 01:30 EST del dia siguiente", next)
	}

	// Cambiar solo la zona replanifica: las 01:30 pasan a ser las de Berlin.
	job.Timezone = "Europe/Berlin"
	if err := e.uc.UpdateJob(e.ctx, job); err != nil {
		t.Fatal(err)
	}
	if n := e.count(t, `SELECT count(*) FROM scheduler.job_definitions WHERE id = $1 AND timezone = 'Europe/Berlin'`, job.ID); n != 1 {
		t.Fatal("la zona editada no se guardo")
	}
	if next, _ := e.schedule(t, job.ID); !next.Equal(time.Date(2026, 11, 2, 0, 30, 0, 0, time.UTC)) {
		t.Fatalf("tras cambiar la zona: %v, se esperaba 01:30 CET", next)
	}
}

func TestReconciliarLeeLaZonaDeLaBase(t *testing.T) {
	e := setup(t)
	now := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	e.clock.set(now)
	daily := "0 8 * * *"
	lima := e.insertLegacyJob(t, domain.JobTypeCron, &daily, nil, time.Date(2026, 9, 14, 8, 0, 0, 0, time.UTC))
	broken := e.insertLegacyJob(t, domain.JobTypeCron, &daily, nil, now.Add(time.Hour))
	for id, tz := range map[uuid.UUID]string{lima: "America/Lima", broken: "Mars/Olympus_Mons"} {
		if _, err := e.pool.Exec(context.Background(), `UPDATE scheduler.job_definitions SET timezone = $1 WHERE id = $2`, tz, id); err != nil {
			t.Fatal(err)
		}
	}
	fixed, err := e.uc.ReconcileCronSchedules(e.ctx)
	if err != nil || fixed != 2 {
		t.Fatalf("corregidos %d (%v), se esperaban 2", fixed, err)
	}
	if next, _ := e.schedule(t, lima); !next.Equal(time.Date(2026, 9, 13, 13, 0, 0, 0, time.UTC)) {
		t.Fatalf("Lima: next_run_at %v, se esperaban las 08:00 de Lima", next)
	}
	if n := e.count(t, `SELECT count(*) FROM scheduler.job_definitions WHERE id = $1 AND NOT is_active`, broken); n != 1 {
		t.Fatal("un cron con una zona que no carga se desactiva")
	}
}
