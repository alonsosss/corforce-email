package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/scheduler/internal/domain"
	"github.com/google/uuid"
)

// addHandlerTask guarda una tarea de la empresa con el estado, la hora de disparo y el manejador
// que pida la prueba.
func (f *fixture) addHandlerTask(status string, trigger time.Time, handler string) domain.ScheduledTask {
	task := domain.ScheduledTask{
		ID: uuid.New(), TenantID: f.tenantID, Name: "Poda", TriggerAt: trigger,
		Handler: handler, Status: status, CreatedAt: f.clock.now(),
	}
	f.store.tasks[task.ID] = task
	return task
}

func (f *fixture) task(id uuid.UUID) domain.ScheduledTask {
	f.t.Helper()
	task, ok := f.store.tasks[id]
	if !ok {
		f.t.Fatalf("no existe la tarea %s", id)
	}
	return task
}

func TestUnaTareaVencidaSeMarcaYSeDespachaEnLaMismaTransaccion(t *testing.T) {
	f := newFixture(t)
	task := f.addHandlerTask(domain.TaskStatusScheduled, f.clock.now().Add(-time.Minute), tenantHandler)
	// Una que aun no vence no se toca.
	future := f.addHandlerTask(domain.TaskStatusScheduled, f.clock.now().Add(time.Hour), tenantHandler)

	f.uc.ProcessPendingTasks(context.Background())

	got := f.task(task.ID)
	if got.Status != domain.TaskStatusExecuted {
		t.Fatalf("estado: %s, se esperaba %s", got.Status, domain.TaskStatusExecuted)
	}
	if got.ExecutedAt == nil || !got.ExecutedAt.Equal(f.clock.now()) {
		t.Fatalf("executed_at lleva la hora del caso de uso: %v", got.ExecutedAt)
	}
	if f.task(future.ID).Status != domain.TaskStatusScheduled {
		t.Fatal("una tarea que aun no vence no se despacha")
	}
	events := f.eventsOf("scheduler.task.started")
	if len(events) != 1 || events[0].execID != task.ID {
		t.Fatalf("un solo despacho, el de la tarea vencida: %+v", events)
	}
	// El despacho y la marca comparten transaccion: el evento existe si y solo si la marca.
	var write *recorded
	for i := range f.store.writes {
		write = &f.store.writes[i]
	}
	if write != nil && write.tx != events[0].tx {
		t.Fatalf("el evento va en otra transaccion que la escritura: %d y %d", events[0].tx, write.tx)
	}
}

func TestUnaTareaCanceladaEntreLaLecturaYLaEscrituraSigueCancelada(t *testing.T) {
	f := newFixture(t)
	task := f.addHandlerTask(domain.TaskStatusScheduled, f.clock.now().Add(-time.Minute), tenantHandler)
	// La cancela otra transaccion justo antes de que el barrido la marque.
	f.store.beforeTaskMark = func() {
		f.store.mu.Lock()
		defer f.store.mu.Unlock()
		stored := f.store.tasks[task.ID]
		stored.Status = domain.TaskStatusCancelled
		f.store.tasks[task.ID] = stored
	}

	f.uc.ProcessPendingTasks(context.Background())

	if got := f.task(task.ID); got.Status != domain.TaskStatusCancelled || got.ExecutedAt != nil {
		t.Fatalf("la cancelada no se reabre: %s, executed_at %v", got.Status, got.ExecutedAt)
	}
	if events := f.eventsOf("scheduler.task.started"); len(events) != 0 {
		t.Fatalf("una tarea que no se marco no se despacha: %+v", events)
	}
}

func TestUnaTareaConManejadorFueraDelCatalogoSeCancelaConSuMotivo(t *testing.T) {
	f := newFixture(t)
	task := f.addHandlerTask(domain.TaskStatusScheduled, f.clock.now().Add(-time.Minute), "se.retiro")

	f.uc.ProcessPendingTasks(context.Background())

	got := f.task(task.ID)
	if got.Status != domain.TaskStatusCancelled {
		t.Fatalf("estado: %s, se esperaba %s", got.Status, domain.TaskStatusCancelled)
	}
	if got.FailureReason == nil || *got.FailureReason != domain.FailureHandlerNotAllowed {
		t.Fatalf("motivo: %v, se esperaba %s", got.FailureReason, domain.FailureHandlerNotAllowed)
	}
	if got.ExecutedAt != nil {
		t.Fatal("no se marca ejecutada: nadie la ejecuto")
	}
	if events := f.eventsOf("scheduler.task.started"); len(events) != 0 {
		t.Fatalf("no se despacha a nadie: %+v", events)
	}
}

// Un manejador de solo plataforma tampoco vale para una tarea, que siempre es de empresa.
func TestProgramarUnaTareaExigeUnManejadorDelCatalogo(t *testing.T) {
	f := newFixture(t)
	for name, handler := range map[string]string{
		"desconocido":        "no.existe",
		"solo de plataforma": platformHandler,
	} {
		task := &domain.ScheduledTask{
			TenantID: f.tenantID, Name: "Poda", TriggerAt: f.clock.now().Add(time.Hour), Handler: handler,
		}
		err := f.uc.ScheduleTask(context.Background(), task)
		if !errors.Is(err, domain.ErrHandlerNotAllowed) {
			t.Errorf("%s: %v, se esperaba ErrHandlerNotAllowed", name, err)
		}
		var ferr *domain.FieldError
		if !errors.As(err, &ferr) || ferr.Field != domain.FieldHandler || ferr.Rule != domain.RuleNotAllowed {
			t.Errorf("%s: el error nombra el campo y la regla: %+v", name, ferr)
		}
		if len(f.store.tasks) != 0 {
			t.Fatalf("%s: no se guarda nada", name)
		}
	}

	task := &domain.ScheduledTask{
		TenantID: f.tenantID, Name: "Poda", TriggerAt: f.clock.now().Add(time.Hour), Handler: tenantHandler,
	}
	if err := f.uc.ScheduleTask(context.Background(), task); err != nil {
		t.Fatalf("un manejador declarado se acepta: %v", err)
	}
	if f.task(task.ID).Status != domain.TaskStatusScheduled {
		t.Fatal("queda programada")
	}
}

// Si el despacho no se puede encolar, la marca se deshace con el: la tarea vuelve a estar
// pendiente y sale en la pasada siguiente, en vez de quedar ejecutada sin despachar.
func TestUnDespachoQueNoSeEncolaDeshaceLaMarca(t *testing.T) {
	f := newFixture(t)
	task := f.addHandlerTask(domain.TaskStatusScheduled, f.clock.now().Add(-time.Minute), tenantHandler)
	f.store.failPublish = errors.New("outbox caida")

	f.uc.ProcessPendingTasks(context.Background())

	if got := f.task(task.ID); got.Status != domain.TaskStatusScheduled || got.ExecutedAt != nil {
		t.Fatalf("la tarea sigue pendiente: %s, executed_at %v", got.Status, got.ExecutedAt)
	}
	f.store.failPublish = nil
	f.uc.ProcessPendingTasks(context.Background())
	if got := f.task(task.ID); got.Status != domain.TaskStatusExecuted {
		t.Fatalf("y sale en la pasada siguiente: %s", got.Status)
	}
	if events := f.eventsOf("scheduler.task.started"); len(events) != 1 {
		t.Fatalf("un solo despacho: %+v", events)
	}
}
