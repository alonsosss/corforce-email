-- Schema: automations | Service: automations
--
-- Automatizaciones de la empresa: el envio del correo del doble opt-in que pide contacts
-- y los flujos de marketing disparados por eventos, con sus ejecuciones por contacto.
--
-- Idempotente y aditiva: se puede reejecutar sobre cualquier base de empresa.

CREATE SCHEMA IF NOT EXISTS automations;

-- Trigger de updated_at. Misma definicion que en los demas esquemas de la base de
-- empresa; CREATE OR REPLACE permite que cualquiera la aplique en cualquier orden.
CREATE OR REPLACE FUNCTION update_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- ── Doble opt-in ─────────────────────────────────────────────────────────────

-- Una fila por empresa. Activado exige plantilla y remitente.
CREATE TABLE IF NOT EXISTS automations.doi_settings (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    enabled boolean NOT NULL DEFAULT false,
    template_id uuid,
    from_email varchar(320) NOT NULL DEFAULT '',
    from_name varchar(200) NOT NULL DEFAULT '',
    reply_to varchar(320) NOT NULL DEFAULT '',
    updated_by uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT automations_doi_settings_tenant_key UNIQUE (tenant_id),
    CONSTRAINT automations_doi_settings_enabled_check CHECK (
        NOT enabled OR (template_id IS NOT NULL AND from_email <> '')
    )
);

DO $$
BEGIN
    CREATE TRIGGER trg_automations_doi_settings_updated
        BEFORE UPDATE ON automations.doi_settings
        FOR EACH ROW EXECUTE FUNCTION update_updated_at();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- Un intento por evento contacts.consent.requested (event_id unico: una reentrega no
-- envia dos veces). El enlace de confirmacion NO se guarda: es una credencial y vuelve a
-- llegar con cada reentrega del evento. Mientras esta pending guarda la plantilla y el
-- remitente con los que se reclamo, para que todo reintento pida exactamente lo mismo a
-- transactional con la misma clave (doi:<event_id>).
CREATE TABLE IF NOT EXISTS automations.doi_deliveries (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    event_id varchar(200) NOT NULL,
    contact_id uuid NOT NULL,
    status varchar(20) NOT NULL DEFAULT 'pending',
    reason text NOT NULL DEFAULT '',
    message_id uuid,
    attempts integer NOT NULL DEFAULT 0,
    template_id uuid,
    from_email varchar(320) NOT NULL DEFAULT '',
    from_name varchar(200) NOT NULL DEFAULT '',
    reply_to varchar(320) NOT NULL DEFAULT '',
    sent_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT automations_doi_deliveries_event_key UNIQUE (event_id),
    CONSTRAINT automations_doi_deliveries_status_check CHECK (
        status IN ('pending', 'sent', 'skipped', 'failed')
    ),
    CONSTRAINT automations_doi_deliveries_attempts_check CHECK (attempts >= 0),
    CONSTRAINT automations_doi_deliveries_pending_check CHECK (
        status <> 'pending' OR (template_id IS NOT NULL AND from_email <> '')
    ),
    CONSTRAINT automations_doi_deliveries_sent_check CHECK (status <> 'sent' OR sent_at IS NOT NULL)
);

-- El limite por contacto cuenta los envios en vuelo y los hechos de una ventana.
CREATE INDEX IF NOT EXISTS idx_automations_doi_deliveries_contact
    ON automations.doi_deliveries (tenant_id, contact_id, created_at DESC)
    WHERE status IN ('pending', 'sent');

CREATE INDEX IF NOT EXISTS idx_automations_doi_deliveries_created
    ON automations.doi_deliveries (tenant_id, created_at DESC);

DO $$
BEGIN
    CREATE TRIGGER trg_automations_doi_deliveries_updated
        BEFORE UPDATE ON automations.doi_deliveries
        FOR EACH ROW EXECUTE FUNCTION update_updated_at();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- ── Flujos ───────────────────────────────────────────────────────────────────

-- steps es la lista lineal de pasos ya validada por el dominio (1..20). Un paso de envio
-- guarda la version de plantilla fijada al activar.
CREATE TABLE IF NOT EXISTS automations.workflows (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    name varchar(200) NOT NULL,
    description text NOT NULL DEFAULT '',
    status varchar(20) NOT NULL DEFAULT 'draft',
    trigger_type varchar(40) NOT NULL,
    trigger_campaign_id uuid,
    list_id uuid,
    re_entry boolean NOT NULL DEFAULT false,
    steps jsonb NOT NULL,
    pause_reason text NOT NULL DEFAULT '',
    created_by uuid NOT NULL,
    activated_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT automations_workflows_status_check CHECK (
        status IN ('draft', 'active', 'paused', 'archived')
    ),
    CONSTRAINT automations_workflows_trigger_check CHECK (
        trigger_type IN ('contact.created', 'consent.granted', 'email.clicked')
    ),
    CONSTRAINT automations_workflows_trigger_campaign_check CHECK (
        trigger_campaign_id IS NULL OR trigger_type = 'email.clicked'
    ),
    CONSTRAINT automations_workflows_steps_check CHECK (
        jsonb_typeof(steps) = 'array' AND jsonb_array_length(steps) BETWEEN 1 AND 20
    )
);

-- Nombre unico por empresa sin distinguir mayusculas.
CREATE UNIQUE INDEX IF NOT EXISTS uq_automations_workflows_tenant_name
    ON automations.workflows (tenant_id, lower(name));

CREATE INDEX IF NOT EXISTS idx_automations_workflows_tenant_created
    ON automations.workflows (tenant_id, created_at DESC);

-- Cada evento busca los flujos activos de su disparador.
CREATE INDEX IF NOT EXISTS idx_automations_workflows_active_trigger
    ON automations.workflows (tenant_id, trigger_type) WHERE status = 'active';

DO $$
BEGIN
    CREATE TRIGGER trg_automations_workflows_updated
        BEFORE UPDATE ON automations.workflows
        FOR EACH ROW EXECUTE FUNCTION update_updated_at();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- Una ejecucion es el recorrido de un contacto por un flujo. entry_key es el id del evento
-- si el flujo admite reentrada y 'once' si no: con la segunda restriccion UNIQUE, un
-- contacto entra una sola vez aunque lleguen dos eventos a la vez. lease_token y
-- lease_until reservan el paso para el trabajador que lo ejecuta: solo existen mientras
-- la ejecucion esta running.
CREATE TABLE IF NOT EXISTS automations.runs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    workflow_id uuid NOT NULL REFERENCES automations.workflows (id) ON DELETE CASCADE,
    contact_id uuid NOT NULL,
    trigger_event_id varchar(200) NOT NULL,
    entry_key varchar(200) NOT NULL,
    step_index integer NOT NULL DEFAULT 0,
    status varchar(20) NOT NULL DEFAULT 'waiting',
    next_run_at timestamptz NOT NULL DEFAULT now(),
    attempts integer NOT NULL DEFAULT 0,
    error_code varchar(64) NOT NULL DEFAULT '',
    last_error text NOT NULL DEFAULT '',
    lease_token uuid,
    lease_until timestamptz,
    finished_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT automations_runs_event_key UNIQUE (workflow_id, contact_id, trigger_event_id),
    CONSTRAINT automations_runs_entry_key UNIQUE (workflow_id, contact_id, entry_key),
    CONSTRAINT automations_runs_status_check CHECK (
        status IN ('waiting', 'running', 'completed', 'failed', 'cancelled', 'skipped')
    ),
    CONSTRAINT automations_runs_counts_check CHECK (step_index >= 0 AND attempts >= 0),
    CONSTRAINT automations_runs_lease_check CHECK (
        (status = 'running') = (lease_token IS NOT NULL AND lease_until IS NOT NULL)
    ),
    CONSTRAINT automations_runs_finished_check CHECK (
        (status IN ('completed', 'failed', 'cancelled', 'skipped')) = (finished_at IS NOT NULL)
    )
);

-- El ejecutor busca las debidas (esperando o con la reserva vencida).
CREATE INDEX IF NOT EXISTS idx_automations_runs_due
    ON automations.runs (tenant_id, next_run_at) WHERE status IN ('waiting', 'running');

CREATE INDEX IF NOT EXISTS idx_automations_runs_workflow
    ON automations.runs (workflow_id, created_at DESC);

-- Fallos recientes de un flujo (pausa automatica por bloqueos repetidos).
CREATE INDEX IF NOT EXISTS idx_automations_runs_failed
    ON automations.runs (workflow_id, finished_at) WHERE status = 'failed';

DO $$
BEGIN
    CREATE TRIGGER trg_automations_runs_updated
        BEFORE UPDATE ON automations.runs
        FOR EACH ROW EXECUTE FUNCTION update_updated_at();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- Eventos de disparo ya procesados (deduplicacion por id del envelope). Se podan a los
-- 30 dias, muy por encima de la retencion del stream.
CREATE TABLE IF NOT EXISTS automations.processed_events (
    event_id varchar(200) PRIMARY KEY,
    tenant_id uuid NOT NULL,
    processed_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_automations_processed_events_processed_at
    ON automations.processed_events (processed_at);
