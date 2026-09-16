-- Schema: scheduler | Service: scheduler
-- Despacho real de las tareas puntuales y el trabajo de plataforma que poda la analitica.
--
-- failure_reason: una tarea vencida cuyo manejador ya no esta en el catalogo no se puede
-- despachar (nadie la recogeria) y queda cancelada con el motivo, en vez de marcarse
-- ejecutada sin que nadie haga nada. Una tarea que cancela una persona lo deja en NULL.
--
-- El trabajo sembrado es de plataforma (tenant_id NULL): lo define la plataforma, la
-- empresa lo ve en su listado y no puede cambiarlo ni desactivarlo (ErrPlatformJob). Vive
-- aqui, en la migracion canonica del scheduler, porque la tabla es suya; el manejador que
-- apunta lo ejecuta analytics y lo declara services/scheduler/handlers.json.
--
-- next_run_at se siembra en la proxima medianoche UTC, que es la ocurrencia que le
-- corresponde a @daily: asi el alta no dispara una poda en el propio despliegue.
-- Idempotente y solo aditiva.

ALTER TABLE scheduler.scheduled_tasks ADD COLUMN IF NOT EXISTS failure_reason varchar(40);

DO $$
BEGIN
    ALTER TABLE scheduler.scheduled_tasks ADD CONSTRAINT scheduled_tasks_failure_reason_check
        CHECK (failure_reason IS NULL OR failure_reason IN ('handler_not_allowed'));
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

INSERT INTO scheduler.job_definitions
    (tenant_id, name, code, description, job_type, cron_expression, timezone, handler, is_active, max_retries, timeout_seconds)
VALUES
    (NULL, 'Poda de la analitica', 'analytics-retention-prune',
     'Retiene el detalle por mensaje de la analitica el tiempo configurado en la plataforma.',
     'cron', '@daily', 'UTC', 'analytics.retention.prune', true, 2, 300)
ON CONFLICT (code) DO NOTHING;

INSERT INTO scheduler.job_schedules (job_id, next_run_at, is_locked)
SELECT id, date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' + interval '1 day', false
  FROM scheduler.job_definitions
 WHERE code = 'analytics-retention-prune'
ON CONFLICT (job_id) DO NOTHING;
