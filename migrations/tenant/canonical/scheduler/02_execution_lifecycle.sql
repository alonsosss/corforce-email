-- Schema: scheduler | Service: scheduler
-- Ciclo de vida de una ejecucion despachada a un servicio ejecutor.
--
-- deadline_at: limite para recibir el cierre (POST /internal/scheduler/executions/{id}/
-- complete o /fail); el barrido marca failed con motivo timeout las activas que lo pasan.
-- next_attempt_at: hora de un reintento automatico que espera en pending, aun sin plazo.
-- retry_of: la ejecucion fallida que este intento reintenta. El indice unico garantiza un
-- solo reintento por ejecucion, automatico o manual.
-- failure_reason: executor, timeout o handler_not_allowed.
--
-- job_schedules.is_locked, locked_by y locked_at quedan sin escritor: el bloqueo por
-- trabajo es el FOR UPDATE SKIP LOCKED de la transaccion que lo despacha, que se suelta
-- solo si el proceso cae.
-- Idempotente y solo aditiva.

ALTER TABLE scheduler.job_executions ADD COLUMN IF NOT EXISTS deadline_at timestamptz;
ALTER TABLE scheduler.job_executions ADD COLUMN IF NOT EXISTS next_attempt_at timestamptz;
ALTER TABLE scheduler.job_executions ADD COLUMN IF NOT EXISTS retry_of uuid REFERENCES scheduler.job_executions(id);
ALTER TABLE scheduler.job_executions ADD COLUMN IF NOT EXISTS failure_reason varchar(40);

DO $$
BEGIN
    ALTER TABLE scheduler.job_executions ADD CONSTRAINT job_executions_failure_reason_check
        CHECK (failure_reason IS NULL OR failure_reason IN ('executor', 'timeout', 'handler_not_allowed'));
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

CREATE UNIQUE INDEX IF NOT EXISTS uq_job_executions_retry_of
    ON scheduler.job_executions (retry_of) WHERE retry_of IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_job_executions_deadline
    ON scheduler.job_executions (deadline_at)
    WHERE status IN ('pending', 'running') AND deadline_at IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_job_executions_next_attempt
    ON scheduler.job_executions (next_attempt_at)
    WHERE status = 'pending' AND deadline_at IS NULL;

-- Las ejecuciones activas anteriores a esta migracion se despacharon sin plazo: toman el de
-- su trabajo desde que empezaron (o se crearon) para que el barrido las cierre.
UPDATE scheduler.job_executions e
   SET deadline_at = COALESCE(e.started_at, e.created_at) + make_interval(secs => GREATEST(jd.timeout_seconds, 1))
  FROM scheduler.job_definitions jd
 WHERE jd.id = e.job_id
   AND e.status IN ('pending', 'running')
   AND e.deadline_at IS NULL
   AND e.next_attempt_at IS NULL;
