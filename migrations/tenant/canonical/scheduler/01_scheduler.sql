-- Schema: scheduler | Service: scheduler
-- Trabajos programados del tenant: definiciones (tenant_id NULL = trabajo de
-- plataforma visible desde cualquier empresa), ejecuciones, calendario con
-- bloqueo en base para que varias replicas del scheduler no lancen dos veces el
-- mismo trabajo, y tareas puntuales con fecha de disparo.
-- Idempotente: se puede reejecutar sobre cualquier base de tenant.

CREATE SCHEMA IF NOT EXISTS scheduler;

-- Trigger de updated_at. Se define aqui con CREATE OR REPLACE porque la base de
-- tenant no tiene una migracion comun previa; otros esquemas pueden redefinirla
-- con la misma definicion sin conflicto.
CREATE OR REPLACE FUNCTION update_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TABLE IF NOT EXISTS scheduler.job_definitions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid,
    name varchar(255) NOT NULL,
    code varchar(100) NOT NULL,
    description text,
    job_type varchar(50) NOT NULL DEFAULT 'cron',
    cron_expression varchar(100),
    interval_minutes integer,
    handler varchar(255) NOT NULL,
    payload jsonb,
    is_active boolean NOT NULL DEFAULT true,
    max_retries integer NOT NULL DEFAULT 3,
    timeout_seconds integer NOT NULL DEFAULT 300,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT job_definitions_code_key UNIQUE (code)
);

CREATE INDEX IF NOT EXISTS idx_job_definitions_tenant ON scheduler.job_definitions (tenant_id, name);

DO $$
BEGIN
    CREATE TRIGGER trg_job_definitions_updated
        BEFORE UPDATE ON scheduler.job_definitions
        FOR EACH ROW EXECUTE FUNCTION update_updated_at();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- started_at es NULL mientras la ejecucion esta en 'pending' (lanzada a mano y
-- aun no recogida); duration se guarda en milisegundos.
CREATE TABLE IF NOT EXISTS scheduler.job_executions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    job_id uuid NOT NULL REFERENCES scheduler.job_definitions(id),
    tenant_id uuid,
    status varchar(20) NOT NULL DEFAULT 'pending',
    started_at timestamptz,
    completed_at timestamptz,
    duration bigint,
    result jsonb,
    error_message text,
    retry_count integer NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT job_executions_status_check CHECK (status IN ('pending', 'running', 'completed', 'failed', 'cancelled'))
);

CREATE INDEX IF NOT EXISTS idx_job_executions_job ON scheduler.job_executions (job_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_job_executions_active ON scheduler.job_executions (created_at DESC)
    WHERE status IN ('pending', 'running');

-- Una fila por trabajo. El bloqueo (is_locked/locked_by/locked_at) se toma con un
-- UPDATE condicional: solo una replica consigue la fila y lanza la ejecucion.
CREATE TABLE IF NOT EXISTS scheduler.job_schedules (
    job_id uuid PRIMARY KEY REFERENCES scheduler.job_definitions(id) ON DELETE CASCADE,
    next_run_at timestamptz NOT NULL,
    last_run_at timestamptz,
    is_locked boolean NOT NULL DEFAULT false,
    locked_by varchar(255),
    locked_at timestamptz
);

CREATE INDEX IF NOT EXISTS idx_job_schedules_due ON scheduler.job_schedules (is_locked, next_run_at);

CREATE TABLE IF NOT EXISTS scheduler.scheduled_tasks (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    name varchar(255) NOT NULL,
    description text,
    trigger_at timestamptz NOT NULL,
    handler varchar(255) NOT NULL,
    payload jsonb,
    status varchar(20) NOT NULL DEFAULT 'scheduled',
    executed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT scheduled_tasks_status_check CHECK (status IN ('scheduled', 'executed', 'cancelled'))
);

CREATE INDEX IF NOT EXISTS idx_scheduled_tasks_due ON scheduler.scheduled_tasks (status, trigger_at);
CREATE INDEX IF NOT EXISTS idx_scheduled_tasks_tenant ON scheduler.scheduled_tasks (tenant_id, trigger_at);
