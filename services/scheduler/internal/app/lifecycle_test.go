package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/scheduler/internal/domain"
	"github.com/google/uuid"
)

var ctx = context.Background()

func TestManejadorDesconocidoONoPermitidoSeRechaza(t *testing.T) {
	f := newFixture(t)
	five := 5
	newJob := func(handler string) *domain.JobDefinition {
		tenant := f.tenantID
		return &domain.JobDefinition{TenantID: &tenant, Name: "n", Code: handler + "-code", JobType: domain.JobTypeInterval,
			Timezone: domain.DefaultTimezone, IntervalMinutes: &five, Handler: handler, TimeoutSeconds: 60}
	}
	for _, h := range []string{"no.existe", platformHandler} {
		if _, err := f.uc.CreateJob(ctx, newJob(h)); !errors.Is(err, domain.ErrHandlerNotAllowed) {
			t.Errorf("crear con %q: %v", h, err)
		}
	}
	if len(f.store.jobs) != 0 || len(f.store.schedules) != 0 {
		t.Fatal("un trabajo rechazado no debe guardarse")
	}

	own := f.addJob(nil)
	own.Handler = "no.existe"
	if _, err := f.uc.UpdateJob(ctx, &own); !errors.Is(err, domain.ErrHandlerNotAllowed) {
		t.Errorf("editar con un manejador desconocido: %v", err)
	}
	if f.store.jobUpdates != 0 {
		t.Fatal("una edicion rechazada no debe guardarse")
	}

	ok := newJob(tenantHandler)
	if _, err := f.uc.CreateJob(ctx, ok); err != nil {
		t.Fatalf("crear con un manejador del catalogo: %v", err)
	}
	if _, scheduled := f.store.schedules[ok.ID]; !scheduled {
		t.Fatal("el trabajo creado debe quedar en el calendario")
	}
}

func TestElInicioSePublicaEnLaMismaTransaccion(t *testing.T) {
	f := newFixture(t)
	job := f.addJob(nil)
	exec := f.run(job)

	stored := f.exec(exec.ID)
	if stored.Status != domain.StatusRunning || !stored.DeadlineAt.Equal(f.clock.now().Add(120*time.Second)) {
		t.Fatalf("despachada: %s con plazo %v", stored.Status, stored.DeadlineAt)
	}
	started := f.eventsOf("scheduler.job.started")
	if len(started) != 1 || started[0].execID != exec.ID || started[0].timeout != 120 {
		t.Fatalf("evento de inicio: %+v", started)
	}
	if tx := f.store.writes[0].tx; tx == 0 || started[0].tx != tx {
		t.Fatalf("la ejecucion (tx %d) y su evento (tx %d) deben ir en la misma transaccion", tx, started[0].tx)
	}

	f.store.failPublish = errors.New("outbox caida")
	if _, err := f.uc.RunJob(ctx, f.tenantID, job.ID); err == nil {
		t.Fatal("si no se puede encolar el evento, el lanzamiento falla")
	}
	if len(f.store.execs) != 1 {
		t.Fatalf("sin evento no queda ejecucion: hay %d", len(f.store.execs))
	}
}

func TestElPlazoSeAcotaAlMaximoDelManejador(t *testing.T) {
	f := newFixture(t)
	for job, want := range map[int]int{0: 600, 5000: 600, 90: 90} {
		j := f.addJob(func(j *domain.JobDefinition) { j.TimeoutSeconds = job })
		f.run(j)
		events := f.eventsOf("scheduler.job.started")
		if got := events[len(events)-1].timeout; got != want {
			t.Errorf("trabajo con %d s: plazo %d, se esperaba %d", job, got, want)
		}
	}
}

func TestCerrarDosVecesRespondeLoMismo(t *testing.T) {
	f := newFixture(t)
	job := f.addJob(nil)
	exec := f.run(job)
	f.clock.advance(3 * time.Second)

	result := `{"rows":3}`
	first, err := f.uc.CompleteExecution(ctx, exec.ID, f.tenantID, &result)
	if err != nil {
		t.Fatal(err)
	}
	f.clock.advance(time.Minute)
	second, err := f.uc.CompleteExecution(ctx, exec.ID, f.tenantID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != domain.StatusCompleted || second.Status != domain.StatusCompleted ||
		!first.CompletedAt.Equal(*second.CompletedAt) || *second.Result != result || *second.Duration != 3000 {
		t.Fatalf("primer cierre %+v, segundo %+v", first, second)
	}
	if n := len(f.eventsOf("scheduler.job.completed")); n != 1 {
		t.Fatalf("un solo scheduler.job.completed, hubo %d", n)
	}

	other := f.run(job)
	for i := 0; i < 2; i++ {
		got, err := f.uc.FailExecution(ctx, other.ID, f.tenantID, "fallo", true)
		if err != nil || got.Status != domain.StatusFailed {
			t.Fatalf("fallar (vez %d): %v %+v", i+1, err, got)
		}
	}
	if n := len(f.eventsOf("scheduler.job.failed")); n != 1 {
		t.Fatalf("un solo scheduler.job.failed, hubo %d", n)
	}
	retries := 0
	for _, e := range f.store.execs {
		if e.RetryOf != nil && *e.RetryOf == other.ID {
			retries++
		}
	}
	if retries != 1 {
		t.Fatalf("repetir el fallo no programa otro reintento: %d", retries)
	}
}

func TestDeFallidaACompletadaEsConflicto(t *testing.T) {
	f := newFixture(t)
	job := f.addJob(nil)
	exec := f.run(job)
	if _, err := f.uc.FailExecution(ctx, exec.ID, f.tenantID, "no hay datos", false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.uc.CompleteExecution(ctx, exec.ID, f.tenantID, nil); !errors.Is(err, domain.ErrExecutionConflict) {
		t.Fatalf("completar una fallida: %v", err)
	}
	if f.exec(exec.ID).Status != domain.StatusFailed || len(f.eventsOf("scheduler.job.completed")) != 0 {
		t.Fatal("la ejecucion sigue fallida y sin evento de exito")
	}

	done := f.run(job)
	if _, err := f.uc.CompleteExecution(ctx, done.ID, f.tenantID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.uc.FailExecution(ctx, done.ID, f.tenantID, "tarde", true); !errors.Is(err, domain.ErrExecutionConflict) {
		t.Fatalf("fallar una completada: %v", err)
	}
	if _, err := f.uc.CompleteExecution(ctx, done.ID, uuid.New(), nil); !errors.Is(err, domain.ErrExecutionNotFound) {
		t.Fatalf("otra empresa no ve la ejecucion: %v", err)
	}
}

func TestReintentoConEsperaCreciente(t *testing.T) {
	f := newFixture(t)
	job := f.addJob(nil)
	exec := f.run(job)

	lastRetry := func() recorded {
		failed := f.eventsOf("scheduler.job.failed")
		return failed[len(failed)-1]
	}
	id := exec.ID
	for n, wait := range []time.Duration{time.Minute, 2 * time.Minute} {
		if _, err := f.uc.FailExecution(ctx, id, f.tenantID, "transitorio", true); err != nil {
			t.Fatal(err)
		}
		retry := lastRetry().retry
		if retry == nil || retry.Status != domain.StatusPending || *retry.RetryOf != id || retry.RetryCount != n+1 ||
			!retry.NextAttemptAt.Equal(f.clock.now().Add(wait)) {
			t.Fatalf("reintento %d: %+v", n+1, retry)
		}
		f.clock.advance(wait - time.Second)
		if got := f.uc.DispatchRetries(ctx); got != 0 {
			t.Fatalf("reintento %d despachado antes de su hora", n+1)
		}
		f.clock.advance(time.Second)
		if got := f.uc.DispatchRetries(ctx); got != 1 {
			t.Fatalf("reintento %d no despachado a su hora: %d", n+1, got)
		}
		dispatched := f.exec(retry.ID)
		if dispatched.Status != domain.StatusRunning || dispatched.DeadlineAt == nil || dispatched.Attempt() != n+2 {
			t.Fatalf("reintento %d despachado: %+v", n+1, dispatched)
		}
		id = retry.ID
	}
	if _, err := f.uc.FailExecution(ctx, id, f.tenantID, "transitorio", true); err != nil {
		t.Fatal(err)
	}
	if lastRetry().retry != nil {
		t.Fatal("agotado max_retries no se programa otro intento")
	}
	if n := len(f.eventsOf("scheduler.job.started")); n != 3 {
		t.Fatalf("tres intentos despachados, hubo %d", n)
	}

	final := f.run(job)
	if _, err := f.uc.FailExecution(ctx, final.ID, f.tenantID, "definitivo", false); err != nil {
		t.Fatal(err)
	}
	if lastRetry().retry != nil {
		t.Fatal("un fallo no reintentable no se reintenta")
	}
}

func TestVencimientoPorTimeout(t *testing.T) {
	f := newFixture(t)
	job := f.addJob(nil)
	exec := f.run(job)

	f.clock.advance(119 * time.Second)
	if got := f.uc.ExpireOverdue(ctx); got != 0 {
		t.Fatalf("vencida antes de su plazo: %d", got)
	}
	f.clock.advance(time.Second)
	if got := f.uc.ExpireOverdue(ctx); got != 1 {
		t.Fatalf("no vencio al llegar su plazo: %d", got)
	}
	expired := f.exec(exec.ID)
	if expired.Status != domain.StatusFailed || *expired.FailureReason != domain.FailureTimeout {
		t.Fatalf("vencida: %s por %v", expired.Status, expired.FailureReason)
	}
	failed := f.eventsOf("scheduler.job.failed")
	if len(failed) != 1 || failed[0].retry == nil {
		t.Fatalf("el vencimiento publica el fallo y aplica los reintentos: %+v", failed)
	}
	if got := f.uc.ExpireOverdue(ctx); got != 0 {
		t.Fatalf("un segundo barrido no vuelve a marcarla: %d", got)
	}

	if _, err := f.uc.CompleteExecution(ctx, exec.ID, f.tenantID, nil); !errors.Is(err, domain.ErrExecutionConflict) {
		t.Fatalf("el cierre tardio de una vencida: %v", err)
	}
	if _, err := f.uc.FailExecution(ctx, exec.ID, f.tenantID, "tarde", true); err != nil {
		t.Fatalf("el fallo tardio de una vencida responde lo mismo: %v", err)
	}
	if len(f.eventsOf("scheduler.job.failed")) != 1 {
		t.Fatal("el fallo tardio no publica ni reintenta de nuevo")
	}
}

func TestCanceladaNoSeReabre(t *testing.T) {
	f := newFixture(t)
	job := f.addJob(nil)
	exec := f.run(job)
	for i := 0; i < 2; i++ {
		if err := f.uc.CancelExecution(ctx, exec.ID, f.tenantID); err != nil {
			t.Fatalf("cancelar (vez %d): %v", i+1, err)
		}
	}
	if got, err := f.uc.CompleteExecution(ctx, exec.ID, f.tenantID, nil); err != nil || got.Status != domain.StatusCancelled {
		t.Fatalf("completar una cancelada: %v %+v", err, got)
	}
	if got, err := f.uc.FailExecution(ctx, exec.ID, f.tenantID, "x", true); err != nil || got.Status != domain.StatusCancelled {
		t.Fatalf("fallar una cancelada: %v %+v", err, got)
	}
	f.clock.advance(time.Hour)
	if got := f.uc.ExpireOverdue(ctx); got != 0 {
		t.Fatal("una cancelada no vence")
	}
	if _, err := f.uc.RetryFailedExecution(ctx, exec.ID, f.tenantID); !errors.Is(err, domain.ErrExecutionNotRetryable) {
		t.Fatalf("reintentar una cancelada: %v", err)
	}
	if f.exec(exec.ID).Status != domain.StatusCancelled || len(f.store.events) != 1 {
		t.Fatalf("sigue cancelada y sin mas eventos que el inicio: %d", len(f.store.events))
	}

	done := f.run(job)
	if _, err := f.uc.CompleteExecution(ctx, done.ID, f.tenantID, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.uc.CancelExecution(ctx, done.ID, f.tenantID); !errors.Is(err, domain.ErrExecutionClosed) {
		t.Fatalf("cancelar una completada: %v", err)
	}
}

func TestReintentoManual(t *testing.T) {
	f := newFixture(t)
	job := f.addJob(nil)
	exec := f.run(job)
	if _, err := f.uc.FailExecution(ctx, exec.ID, f.tenantID, "definitivo", false); err != nil {
		t.Fatal(err)
	}
	retry, err := f.uc.RetryFailedExecution(ctx, exec.ID, f.tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if retry.Status != domain.StatusRunning || *retry.RetryOf != exec.ID || retry.RetryCount != 1 {
		t.Fatalf("reintento manual: %+v", retry)
	}
	if _, err := f.uc.RetryFailedExecution(ctx, exec.ID, f.tenantID); !errors.Is(err, domain.ErrAlreadyRetried) {
		t.Fatalf("una ejecucion solo tiene un reintento: %v", err)
	}

	auto := f.run(job)
	if _, err := f.uc.FailExecution(ctx, auto.ID, f.tenantID, "transitorio", true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.uc.RetryFailedExecution(ctx, auto.ID, f.tenantID); !errors.Is(err, domain.ErrAlreadyRetried) {
		t.Fatalf("con un reintento automatico ya programado: %v", err)
	}

	spent := f.addJob(func(j *domain.JobDefinition) { j.MaxRetries = 0 })
	e := f.run(spent)
	if _, err := f.uc.FailExecution(ctx, e.ID, f.tenantID, "x", true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.uc.RetryFailedExecution(ctx, e.ID, f.tenantID); !errors.Is(err, domain.ErrMaxRetriesExceeded) {
		t.Fatalf("sin presupuesto de reintentos: %v", err)
	}
}

func TestProcessDueJobs(t *testing.T) {
	f := newFixture(t)
	now := f.clock.now()
	due := f.addJob(nil)
	inactive := f.addJob(func(j *domain.JobDefinition) { j.IsActive = false })
	orphan := f.addJob(func(j *domain.JobDefinition) { j.Handler = "retirado.del.catalogo" })
	once := f.addJob(func(j *domain.JobDefinition) { j.JobType = domain.JobTypeOneTime })
	for _, j := range []domain.JobDefinition{due, inactive, orphan, once} {
		f.store.schedules[j.ID] = domain.JobSchedule{JobID: j.ID, NextRunAt: now.Add(-time.Second)}
	}

	f.uc.ProcessDueJobs(ctx)
	f.uc.ProcessDueJobs(ctx)

	byJob := map[string][]domain.JobExecution{}
	for _, e := range f.store.execs {
		byJob[e.JobID.String()] = append(byJob[e.JobID.String()], e)
	}
	if got := byJob[due.ID.String()]; len(got) != 1 || got[0].Status != domain.StatusRunning {
		t.Fatalf("el trabajo vencido se lanza una sola vez: %+v", got)
	}
	// La rejilla cuenta desde la hora prevista (un segundo antes de la pasada), no desde ella.
	if want := now.Add(-time.Second).Add(5 * time.Minute); !f.store.schedules[due.ID].NextRunAt.Equal(want) {
		t.Fatalf("siguiente pasada: %v, se esperaba %v", f.store.schedules[due.ID].NextRunAt, want)
	}
	if got := byJob[inactive.ID.String()]; len(got) != 0 {
		t.Fatal("un trabajo inactivo no se lanza")
	}
	got := byJob[orphan.ID.String()]
	if len(got) != 1 || got[0].Status != domain.StatusFailed || *got[0].FailureReason != domain.FailureHandlerNotAllowed {
		t.Fatalf("un manejador fuera del catalogo deja una ejecucion fallida: %+v", got)
	}
	if len(byJob[once.ID.String()]) != 1 || f.store.jobs[once.ID].IsActive {
		t.Fatal("un trabajo de una sola vez se lanza y queda inactivo")
	}
	for _, ev := range f.eventsOf("scheduler.job.failed") {
		if ev.retry != nil {
			t.Fatal("sin manejador permitido no hay reintento")
		}
	}
}

func TestReintentoDeUnTrabajoDesactivadoSeCancela(t *testing.T) {
	f := newFixture(t)
	job := f.addJob(nil)
	exec := f.run(job)
	if _, err := f.uc.FailExecution(ctx, exec.ID, f.tenantID, "transitorio", true); err != nil {
		t.Fatal(err)
	}
	retry := f.eventsOf("scheduler.job.failed")[0].retry
	if err := f.uc.DisableJob(ctx, job.ID, f.tenantID); err != nil {
		t.Fatal(err)
	}
	f.clock.advance(time.Hour)
	if got := f.uc.DispatchRetries(ctx); got != 1 {
		t.Fatalf("barrido: %d", got)
	}
	if f.exec(retry.ID).Status != domain.StatusCancelled || len(f.eventsOf("scheduler.job.started")) != 1 {
		t.Fatal("el reintento de un trabajo desactivado se cancela sin despacharse")
	}
}
