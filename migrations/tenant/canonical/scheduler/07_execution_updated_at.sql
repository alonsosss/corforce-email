-- Schema: scheduler | Service: scheduler
--
-- job_executions, job_schedules y scheduled_tasks se actualizan en su ciclo de vida
-- (MarkExecuted, CancelUndispatchable, UpdateNextRun...) pero, a diferencia de
-- job_definitions, nunca tuvieron updated_at ni su trigger. Se agregan aqui, aditivo:
-- ADD COLUMN con DEFAULT now() dejando la columna consistente tambien en las filas ya
-- existentes.

ALTER TABLE scheduler.job_executions ADD COLUMN IF NOT EXISTS updated_at timestamptz NOT NULL DEFAULT now();
ALTER TABLE scheduler.job_schedules ADD COLUMN IF NOT EXISTS updated_at timestamptz NOT NULL DEFAULT now();
ALTER TABLE scheduler.scheduled_tasks ADD COLUMN IF NOT EXISTS updated_at timestamptz NOT NULL DEFAULT now();

DO $$
BEGIN
    CREATE TRIGGER trg_job_executions_updated
        BEFORE UPDATE ON scheduler.job_executions
        FOR EACH ROW EXECUTE FUNCTION update_updated_at();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

DO $$
BEGIN
    CREATE TRIGGER trg_job_schedules_updated
        BEFORE UPDATE ON scheduler.job_schedules
        FOR EACH ROW EXECUTE FUNCTION update_updated_at();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

DO $$
BEGIN
    CREATE TRIGGER trg_scheduled_tasks_updated
        BEFORE UPDATE ON scheduler.scheduled_tasks
        FOR EACH ROW EXECUTE FUNCTION update_updated_at();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;
