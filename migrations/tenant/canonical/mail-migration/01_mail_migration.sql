-- Schema: mail_migration | Service: mail-migration
--
-- Trabajos de migracion de un buzon desde otro proveedor por IMAP (docs/adr/0002). Cada fila es
-- un trabajo: el buzon destino de la empresa, el origen (servidor, usuario y contrasena) y el
-- progreso que informa el ejecutor. La contrasena de origen es una credencial de terceros: se
-- guarda cifrada con AES-256-GCM (pkg/crypto.KeyRing, MAIL_ENCRYPTION_KEY) solo mientras el
-- trabajo esta pendiente o en curso, nunca sale por la API y jobs_credential_check impide que
-- una fila en estado final la conserve.
--
-- Idempotente y aditiva: se puede reejecutar sobre cualquier base de empresa.

CREATE SCHEMA IF NOT EXISTS mail_migration;

-- Trigger de updated_at. Misma definicion que en los demas esquemas de la base de
-- empresa; CREATE OR REPLACE permite que cualquiera la aplique en cualquier orden.
CREATE OR REPLACE FUNCTION update_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TABLE IF NOT EXISTS mail_migration.jobs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    -- El buzon vive en la celda (mail-directory): aqui solo se guarda su id y el nombre con
    -- el que entra, tal como estaba al lanzar el trabajo.
    mailbox_id uuid NOT NULL,
    mailbox_username varchar(320) NOT NULL,
    source_host varchar(253) NOT NULL,
    source_port integer NOT NULL,
    source_tls varchar(10) NOT NULL,
    source_username varchar(320) NOT NULL,
    source_password_enc bytea,
    status varchar(12) NOT NULL DEFAULT 'pending',
    phase varchar(10) NOT NULL DEFAULT '',
    progress jsonb NOT NULL DEFAULT '{}',
    attempt integer NOT NULL DEFAULT 0,
    last_error_code varchar(40) NOT NULL DEFAULT '',
    last_error_message varchar(300) NOT NULL DEFAULT '',
    requested_by uuid NOT NULL,
    runner_id varchar(100) NOT NULL DEFAULT '',
    lease_id uuid,
    lease_expires_at timestamptz,
    heartbeat_at timestamptz,
    cancel_requested_at timestamptz,
    started_at timestamptz,
    finished_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT mail_migration_jobs_port_check CHECK (source_port BETWEEN 1 AND 65535),
    CONSTRAINT mail_migration_jobs_tls_check CHECK (source_tls IN ('ssl', 'starttls', 'none')),
    CONSTRAINT mail_migration_jobs_status_check CHECK (status IN ('pending', 'running', 'succeeded', 'failed', 'cancelled')),
    CONSTRAINT mail_migration_jobs_phase_check CHECK (phase IN ('', 'initial', 'catchup')),
    CONSTRAINT mail_migration_jobs_progress_check CHECK (jsonb_typeof(progress) = 'object'),
    CONSTRAINT mail_migration_jobs_attempt_check CHECK (attempt >= 0),
    CONSTRAINT mail_migration_jobs_credential_check CHECK (
        status IN ('pending', 'running') OR source_password_enc IS NULL
    ),
    CONSTRAINT mail_migration_jobs_finished_check CHECK (
        (status IN ('succeeded', 'failed', 'cancelled')) = (finished_at IS NOT NULL)
    ),
    CONSTRAINT mail_migration_jobs_lease_check CHECK (
        (status = 'running') = (lease_id IS NOT NULL AND lease_expires_at IS NOT NULL)
    )
);

DO $$
BEGIN
    CREATE TRIGGER trg_mail_migration_jobs_updated
        BEFORE UPDATE ON mail_migration.jobs
        FOR EACH ROW EXECUTE FUNCTION update_updated_at();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- Un solo trabajo activo por buzon: dos copias a la vez sobre el mismo destino solo duplicarian
-- trabajo y mezclarian sus contadores.
CREATE UNIQUE INDEX IF NOT EXISTS uq_mail_migration_jobs_one_active_per_mailbox
    ON mail_migration.jobs (tenant_id, mailbox_id) WHERE status IN ('pending', 'running');

-- Lo que recorre el reclamo del ejecutor y el conteo del limite de la empresa.
CREATE INDEX IF NOT EXISTS idx_mail_migration_jobs_active
    ON mail_migration.jobs (tenant_id, created_at) WHERE status IN ('pending', 'running');

CREATE INDEX IF NOT EXISTS idx_mail_migration_jobs_mailbox
    ON mail_migration.jobs (tenant_id, mailbox_id, created_at DESC);
