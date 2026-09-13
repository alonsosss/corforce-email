-- Schema: campaigns | Service: campaigns
--
-- Campanas de marketing de la empresa: su ciclo de vida, los lotes en que el orquestador
-- reparte la audiencia de contacts para entregarla a la via de marketing de
-- transactional, y lo que hace falta para contar sus estadisticas una sola vez.
--
-- Idempotente y aditiva: se puede reejecutar sobre cualquier base de empresa.

CREATE SCHEMA IF NOT EXISTS campaigns;

-- Trigger de updated_at. Misma definicion que en los demas esquemas de la base de
-- empresa; CREATE OR REPLACE permite que cualquiera la aplique en cualquier orden.
CREATE OR REPLACE FUNCTION update_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- template_version se fija al programar o iniciar: fuera de borrador siempre hay una.
-- audience es {list_ids, segment_ids, exclude_segment_ids}; contacts la resuelve en cada
-- lote. resume_after aplaza el siguiente lote (429 de transactional o espera tras un
-- fallo transitorio). targeted, accepted y suppressed los suma el orquestador; el resto
-- de contadores, los eventos de transactional.
CREATE TABLE IF NOT EXISTS campaigns.campaigns (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    name varchar(200) NOT NULL,
    description text NOT NULL DEFAULT '',
    status varchar(20) NOT NULL DEFAULT 'draft',
    pause_reason text NOT NULL DEFAULT '',
    failure_reason text NOT NULL DEFAULT '',
    template_id uuid NOT NULL,
    template_version integer,
    from_email varchar(320) NOT NULL,
    from_name varchar(200) NOT NULL DEFAULT '',
    reply_to varchar(320) NOT NULL DEFAULT '',
    audience jsonb NOT NULL,
    scheduled_at timestamptz,
    started_at timestamptz,
    completed_at timestamptz,
    resume_after timestamptz,
    targeted bigint NOT NULL DEFAULT 0,
    accepted bigint NOT NULL DEFAULT 0,
    suppressed bigint NOT NULL DEFAULT 0,
    sent bigint NOT NULL DEFAULT 0,
    delivered bigint NOT NULL DEFAULT 0,
    bounced bigint NOT NULL DEFAULT 0,
    complained bigint NOT NULL DEFAULT 0,
    opened bigint NOT NULL DEFAULT 0,
    clicked bigint NOT NULL DEFAULT 0,
    unsubscribed bigint NOT NULL DEFAULT 0,
    failed bigint NOT NULL DEFAULT 0,
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT campaigns_campaigns_status_check CHECK (
        status IN ('draft', 'scheduled', 'sending', 'paused', 'completed', 'cancelled', 'failed')
    ),
    CONSTRAINT campaigns_campaigns_version_check CHECK (template_version IS NULL OR template_version > 0),
    CONSTRAINT campaigns_campaigns_pinned_check CHECK (status = 'draft' OR template_version IS NOT NULL),
    CONSTRAINT campaigns_campaigns_scheduled_check CHECK (status <> 'scheduled' OR scheduled_at IS NOT NULL),
    CONSTRAINT campaigns_campaigns_audience_check CHECK (jsonb_typeof(audience) = 'object'),
    CONSTRAINT campaigns_campaigns_counters_check CHECK (
        targeted >= 0 AND accepted >= 0 AND suppressed >= 0 AND sent >= 0 AND delivered >= 0
        AND bounced >= 0 AND complained >= 0 AND opened >= 0 AND clicked >= 0
        AND unsubscribed >= 0 AND failed >= 0
    )
);

-- Nombre unico por empresa sin distinguir mayusculas.
CREATE UNIQUE INDEX IF NOT EXISTS uq_campaigns_tenant_name
    ON campaigns.campaigns (tenant_id, lower(name));

CREATE INDEX IF NOT EXISTS idx_campaigns_tenant_created
    ON campaigns.campaigns (tenant_id, created_at DESC);

-- El orquestador busca las programadas vencidas y las que estan en envio.
CREATE INDEX IF NOT EXISTS idx_campaigns_due
    ON campaigns.campaigns (tenant_id, scheduled_at) WHERE status = 'scheduled';
CREATE INDEX IF NOT EXISTS idx_campaigns_sending
    ON campaigns.campaigns (tenant_id, resume_after) WHERE status = 'sending';

DO $$
BEGIN
    CREATE TRIGGER trg_campaigns_campaigns_updated
        BEFORE UPDATE ON campaigns.campaigns
        FOR EACH ROW EXECUTE FUNCTION update_updated_at();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- Un lote es una pagina de la audiencia y una peticion al lote de transactional con la
-- clave campaign:<campaign_id>:batch:<seq>. Se crea pendiente y se confirma ANTES de
-- llamar a nadie; page guarda los destinatarios fijados antes del primer envio para que
-- todo reintento mande exactamente lo mismo, y se vacia al cerrarse el lote.
-- leased_until/lease_token reservan el lote para el trabajador que lo esta enviando.
CREATE TABLE IF NOT EXISTS campaigns.batches (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    campaign_id uuid NOT NULL REFERENCES campaigns.campaigns (id) ON DELETE CASCADE,
    seq integer NOT NULL,
    cursor_in text,
    cursor_out text,
    page jsonb,
    recipients integer NOT NULL DEFAULT 0,
    status varchar(20) NOT NULL DEFAULT 'pending',
    accepted integer NOT NULL DEFAULT 0,
    suppressed integer NOT NULL DEFAULT 0,
    attempts integer NOT NULL DEFAULT 0,
    last_error text NOT NULL DEFAULT '',
    leased_until timestamptz,
    lease_token uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT campaigns_batches_campaign_seq_key UNIQUE (campaign_id, seq),
    CONSTRAINT campaigns_batches_status_check CHECK (status IN ('pending', 'delivered', 'failed')),
    CONSTRAINT campaigns_batches_seq_check CHECK (seq > 0),
    CONSTRAINT campaigns_batches_counts_check CHECK (
        recipients >= 0 AND accepted >= 0 AND suppressed >= 0 AND attempts >= 0
    ),
    CONSTRAINT campaigns_batches_page_pending_check CHECK (status = 'pending' OR page IS NULL),
    CONSTRAINT campaigns_batches_page_array_check CHECK (page IS NULL OR jsonb_typeof(page) = 'array')
);

-- A lo sumo un lote pendiente por campana: nunca hay dos paginas en vuelo a la vez.
CREATE UNIQUE INDEX IF NOT EXISTS uq_campaigns_batches_one_pending
    ON campaigns.batches (campaign_id) WHERE status = 'pending';

DO $$
BEGIN
    CREATE TRIGGER trg_campaigns_batches_updated
        BEFORE UPDATE ON campaigns.batches
        FOR EACH ROW EXECUTE FUNCTION update_updated_at();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- Eventos de transactional ya contados (deduplicacion por id del envelope). Se podan a
-- los 30 dias, muy por encima de la retencion del stream.
CREATE TABLE IF NOT EXISTS campaigns.processed_events (
    event_id varchar(200) PRIMARY KEY,
    tenant_id uuid NOT NULL,
    processed_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_campaigns_processed_events_processed_at
    ON campaigns.processed_events (processed_at);

-- Primera apertura y primer clic de cada mensaje: opened y clicked cuentan mensajes
-- unicos, no eventos.
CREATE TABLE IF NOT EXISTS campaigns.message_engagement (
    message_id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    campaign_id uuid NOT NULL REFERENCES campaigns.campaigns (id) ON DELETE CASCADE,
    opened_at timestamptz,
    clicked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_campaigns_message_engagement_campaign
    ON campaigns.message_engagement (campaign_id);
