package app

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/services/scheduler/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// sweepBatch acota lo que un barrido cierra o despacha por empresa y vuelta: una base con
// miles de vencidas no acapara el turno de las demas, y lo que quede sale en la siguiente.
const sweepBatch = 100

const timeoutMessage = "no report from the executor before the deadline"

// dispatch deja la ejecucion en running con el plazo de su manejador y encola
// scheduler.job.started. Corre dentro de la transaccion que la crea o la actualiza.
func (uc *SchedulerUseCase) dispatch(ctx context.Context, job *domain.JobDefinition, spec domain.HandlerSpec, exec *domain.JobExecution, now time.Time, create bool) error {
	timeout := spec.EffectiveTimeoutSeconds(job.TimeoutSeconds)
	exec.Dispatch(now, timeout)
	if err := uc.persist(ctx, exec, create); err != nil {
		return err
	}
	return uc.events.JobStarted(ctx, job, exec, timeout)
}

// failUndispatched cierra como fallida una ejecucion que no se puede despachar porque su
// manejador ya no esta permitido. No se reintenta: no hay a quien reintentarla.
func (uc *SchedulerUseCase) failUndispatched(ctx context.Context, job *domain.JobDefinition, exec *domain.JobExecution, now time.Time, cause error, create bool) error {
	if _, err := exec.Fail(now, domain.FailureHandlerNotAllowed, cause.Error()); err != nil {
		return err
	}
	if err := uc.persist(ctx, exec, create); err != nil {
		return err
	}
	return uc.events.JobFailed(ctx, job, exec, nil)
}

func (uc *SchedulerUseCase) persist(ctx context.Context, exec *domain.JobExecution, create bool) error {
	if create {
		return uc.executions.Create(ctx, exec)
	}
	return uc.executions.Update(ctx, exec)
}

// closeFailed guarda una ejecucion recien fallida, programa su reintento si procede y
// encola scheduler.job.failed, todo en la transaccion del llamante.
func (uc *SchedulerUseCase) closeFailed(ctx context.Context, job *domain.JobDefinition, exec *domain.JobExecution, now time.Time, retryable bool) error {
	if err := uc.executions.Update(ctx, exec); err != nil {
		return err
	}
	var retry *domain.JobExecution
	if retryable && uc.shouldRetry(job, exec) {
		retry = exec.NextAttempt(uuid.New(), now, uc.retry.Delay(exec.RetryCount+1))
		if err := uc.executions.Create(ctx, retry); err != nil {
			return err
		}
	}
	return uc.events.JobFailed(ctx, job, exec, retry)
}

// shouldRetry: queda presupuesto de reintentos, el trabajo sigue activo y su manejador
// sigue permitido. Un trabajo desactivado no se reintenta solo.
func (uc *SchedulerUseCase) shouldRetry(job *domain.JobDefinition, exec *domain.JobExecution) bool {
	if !job.IsActive || !job.HasRetriesLeft(exec) {
		return false
	}
	_, err := uc.catalog.Resolve(job.Handler, job.IsPlatform())
	return err == nil
}

// CompleteExecution registra el cierre con exito que informa el ejecutor. Es idempotente:
// una ejecucion ya completada o cancelada se devuelve tal cual, sin evento nuevo.
func (uc *SchedulerUseCase) CompleteExecution(ctx context.Context, id, tenantID uuid.UUID, result *string) (*domain.JobExecution, error) {
	var out *domain.JobExecution
	err := uc.tx.Transact(ctx, func(ctx context.Context) error {
		exec, err := uc.executions.GetForUpdate(ctx, id, tenantID)
		if err != nil {
			return err
		}
		changed, err := exec.Complete(uc.now(), result)
		if err != nil {
			return err
		}
		out = exec
		if !changed {
			return nil
		}
		job, err := uc.jobs.GetByID(ctx, exec.JobID, tenantID)
		if err != nil {
			return err
		}
		if err := uc.executions.Update(ctx, exec); err != nil {
			return err
		}
		return uc.events.JobCompleted(ctx, job, exec)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// FailExecution registra el fallo que informa el ejecutor y, si es reintentable y queda
// presupuesto, programa el siguiente intento. Repetirlo no programa otro.
func (uc *SchedulerUseCase) FailExecution(ctx context.Context, id, tenantID uuid.UUID, message string, retryable bool) (*domain.JobExecution, error) {
	var out *domain.JobExecution
	err := uc.tx.Transact(ctx, func(ctx context.Context) error {
		exec, err := uc.executions.GetForUpdate(ctx, id, tenantID)
		if err != nil {
			return err
		}
		now := uc.now()
		changed, err := exec.Fail(now, domain.FailureExecutor, message)
		if err != nil {
			return err
		}
		out = exec
		if !changed {
			return nil
		}
		job, err := uc.jobs.GetByID(ctx, exec.JobID, tenantID)
		if err != nil {
			return err
		}
		return uc.closeFailed(ctx, job, exec, now, retryable)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// CancelExecution detiene una ejecucion activa de la empresa. Cancelar dos veces no cambia
// nada; una ejecucion terminada no se cancela.
func (uc *SchedulerUseCase) CancelExecution(ctx context.Context, id, tenantID uuid.UUID) error {
	return uc.tx.Transact(ctx, func(ctx context.Context) error {
		exec, err := uc.executions.GetForUpdate(ctx, id, tenantID)
		if err != nil {
			return err
		}
		job, err := uc.jobs.GetByID(ctx, exec.JobID, tenantID)
		if err != nil {
			return err
		}
		if job.IsPlatform() {
			return domain.ErrPlatformJob
		}
		changed, err := exec.Cancel(uc.now())
		if err != nil || !changed {
			return err
		}
		return uc.executions.Update(ctx, exec)
	})
}

// RetryFailedExecution lanza a mano el siguiente intento de una ejecucion fallida. Consume
// el mismo presupuesto (max_retries) que los reintentos automaticos, y una ejecucion solo
// tiene un reintento: si ya lo programo el scheduler, este responde ErrAlreadyRetried.
func (uc *SchedulerUseCase) RetryFailedExecution(ctx context.Context, id, tenantID uuid.UUID) (*domain.JobExecution, error) {
	var out *domain.JobExecution
	err := uc.tx.Transact(ctx, func(ctx context.Context) error {
		exec, err := uc.executions.GetForUpdate(ctx, id, tenantID)
		if err != nil {
			return err
		}
		job, err := uc.jobs.GetByID(ctx, exec.JobID, tenantID)
		if err != nil {
			return err
		}
		if job.IsPlatform() {
			return domain.ErrPlatformJob
		}
		if exec.Status != domain.StatusFailed {
			return domain.ErrExecutionNotRetryable
		}
		if !job.HasRetriesLeft(exec) {
			return domain.ErrMaxRetriesExceeded
		}
		spec, err := uc.catalog.Resolve(job.Handler, job.IsPlatform())
		if err != nil {
			return err
		}
		now := uc.now()
		prev := exec.ID
		next := newExecution(job, now)
		next.RetryCount = exec.RetryCount + 1
		next.RetryOf = &prev
		out = next
		return uc.dispatch(ctx, job, spec, next, now, true)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ExpireOverdue cierra como fallidas por timeout las ejecuciones activas cuyo plazo vencio,
// con la misma politica de reintentos que un fallo informado reintentable.
func (uc *SchedulerUseCase) ExpireOverdue(ctx context.Context) int {
	return uc.sweep(ctx, "vencimiento", func(ctx context.Context, now time.Time) (bool, error) {
		exec, err := uc.executions.ClaimOverdue(ctx, now)
		if err != nil || exec == nil {
			return false, err
		}
		job, err := uc.jobs.GetByID(ctx, exec.JobID, tenantOrNil(exec.TenantID))
		if err != nil {
			return true, err
		}
		if _, err := exec.Fail(now, domain.FailureTimeout, timeoutMessage); err != nil {
			return true, err
		}
		return true, uc.closeFailed(ctx, job, exec, now, true)
	})
}

// DispatchRetries despacha los reintentos en espera cuya hora ya llego.
func (uc *SchedulerUseCase) DispatchRetries(ctx context.Context) int {
	return uc.sweep(ctx, "reintentos", func(ctx context.Context, now time.Time) (bool, error) {
		exec, err := uc.executions.ClaimDispatchable(ctx, now)
		if err != nil || exec == nil {
			return false, err
		}
		job, err := uc.jobs.GetByID(ctx, exec.JobID, tenantOrNil(exec.TenantID))
		if err != nil {
			return true, err
		}
		if !job.IsActive {
			if _, err := exec.Cancel(now); err != nil {
				return true, err
			}
			return true, uc.executions.Update(ctx, exec)
		}
		spec, rerr := uc.catalog.Resolve(job.Handler, job.IsPlatform())
		if rerr != nil {
			return true, uc.failUndispatched(ctx, job, exec, now, rerr, false)
		}
		return true, uc.dispatch(ctx, job, spec, exec, now, false)
	})
}

// sweep repite step, cada vez en su propia transaccion, hasta que no encuentra nada, falla
// o llega a sweepBatch. Una transaccion por fila: la que no se puede cerrar no deshace las
// demas.
func (uc *SchedulerUseCase) sweep(ctx context.Context, name string, step func(ctx context.Context, now time.Time) (bool, error)) int {
	done := 0
	for done < sweepBatch && ctx.Err() == nil {
		found := false
		err := uc.tx.Transact(ctx, func(ctx context.Context) error {
			var err error
			found, err = step(ctx, uc.now())
			return err
		})
		if err != nil {
			if ctx.Err() == nil {
				uc.logger.Error("scheduler: barrido interrumpido", zap.String("barrido", name), zap.Error(err))
			}
			return done
		}
		if !found {
			return done
		}
		done++
	}
	return done
}
