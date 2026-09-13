-- Schema: analytics | Service: analytics
--
-- Agregados de envio de la empresa, alimentados por los eventos de transactional
-- (transactional.email.*) y campaigns (campaigns.campaign.*). Postgres primero: ClickHouse
-- solo con volumen medido y ADR.
--
-- message_facts guarda un hito por mensaje (su primera ocurrencia) para que aperturas y
-- clics cuenten una sola vez y para poder rehacer los agregados; se poda por
-- ANALYTICS_MESSAGE_RETENTION_DAYS. Los agregados diarios no se podan. No se guarda la
-- direccion del destinatario, solo su dominio.
--
-- El dia de todos los agregados es el dia UTC del hecho.
--
-- Idempotente y aditiva: se puede reejecutar sobre cualquier base de empresa.

CREATE SCHEMA IF NOT EXISTS analytics;

-- Trigger de updated_at. Misma definicion que en los demas esquemas de la base de
-- empresa; CREATE OR REPLACE permite que cualquiera la aplique en cualquier orden.
CREATE OR REPLACE FUNCTION update_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- Deduplicacion de la ingesta por id de evento. Se poda a los 30 dias, por encima de la
-- retencion de los streams: un evento no se puede reentregar despues de su poda.
CREATE TABLE IF NOT EXISTS analytics.processed_events (
    event_id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    processed_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_analytics_processed_events_tenant_at
    ON analytics.processed_events (tenant_id, processed_at);

-- Un mensaje: sus dimensiones (fijadas por el primer evento que lo nombra) y el instante
-- de la primera ocurrencia de cada hito. bounce_type es hard si algun rebote fue
-- permanente; soft en cualquier otro caso.
CREATE TABLE IF NOT EXISTS analytics.message_facts (
    message_id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    class varchar(20) NOT NULL,
    campaign_id uuid,
    recipient_domain varchar(253),
    sent_at timestamptz,
    delivered_at timestamptz,
    first_opened_at timestamptz,
    first_clicked_at timestamptz,
    bounced_at timestamptz,
    bounce_type varchar(10),
    complained_at timestamptz,
    unsubscribed_at timestamptz,
    failed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT message_facts_class_check CHECK (class IN ('transactional', 'marketing')),
    CONSTRAINT message_facts_bounce_check CHECK (
        (bounced_at IS NULL AND bounce_type IS NULL)
        OR (bounced_at IS NOT NULL AND bounce_type IN ('hard', 'soft'))
    ),
    CONSTRAINT message_facts_domain_lower CHECK (recipient_domain = lower(recipient_domain))
);

-- La poda recorre por empresa los mensajes sin actividad reciente.
CREATE INDEX IF NOT EXISTS idx_analytics_message_facts_tenant_updated
    ON analytics.message_facts (tenant_id, updated_at);

DO $$
BEGIN
    CREATE TRIGGER trg_analytics_message_facts_updated
        BEFORE UPDATE ON analytics.message_facts
        FOR EACH ROW EXECUTE FUNCTION update_updated_at();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- Agregado por dia y clase de envio.
CREATE TABLE IF NOT EXISTS analytics.daily_class_stats (
    tenant_id uuid NOT NULL,
    day date NOT NULL,
    class varchar(20) NOT NULL,
    sent bigint NOT NULL DEFAULT 0,
    delivered bigint NOT NULL DEFAULT 0,
    bounced_hard bigint NOT NULL DEFAULT 0,
    bounced_soft bigint NOT NULL DEFAULT 0,
    complained bigint NOT NULL DEFAULT 0,
    opened_unique bigint NOT NULL DEFAULT 0,
    clicked_unique bigint NOT NULL DEFAULT 0,
    unsubscribed bigint NOT NULL DEFAULT 0,
    failed bigint NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT daily_class_stats_pkey PRIMARY KEY (tenant_id, day, class),
    CONSTRAINT daily_class_stats_class_check CHECK (class IN ('transactional', 'marketing')),
    CONSTRAINT daily_class_stats_counters_check CHECK (
        sent >= 0 AND delivered >= 0 AND bounced_hard >= 0 AND bounced_soft >= 0
        AND complained >= 0 AND opened_unique >= 0 AND clicked_unique >= 0
        AND unsubscribed >= 0 AND failed >= 0
    )
);

DO $$
BEGIN
    CREATE TRIGGER trg_analytics_daily_class_stats_updated
        BEFORE UPDATE ON analytics.daily_class_stats
        FOR EACH ROW EXECUTE FUNCTION update_updated_at();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- Agregado por dia y campana (solo los mensajes con campana).
CREATE TABLE IF NOT EXISTS analytics.daily_campaign_stats (
    tenant_id uuid NOT NULL,
    day date NOT NULL,
    campaign_id uuid NOT NULL,
    sent bigint NOT NULL DEFAULT 0,
    delivered bigint NOT NULL DEFAULT 0,
    bounced_hard bigint NOT NULL DEFAULT 0,
    bounced_soft bigint NOT NULL DEFAULT 0,
    complained bigint NOT NULL DEFAULT 0,
    opened_unique bigint NOT NULL DEFAULT 0,
    clicked_unique bigint NOT NULL DEFAULT 0,
    unsubscribed bigint NOT NULL DEFAULT 0,
    failed bigint NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT daily_campaign_stats_pkey PRIMARY KEY (tenant_id, day, campaign_id),
    CONSTRAINT daily_campaign_stats_counters_check CHECK (
        sent >= 0 AND delivered >= 0 AND bounced_hard >= 0 AND bounced_soft >= 0
        AND complained >= 0 AND opened_unique >= 0 AND clicked_unique >= 0
        AND unsubscribed >= 0 AND failed >= 0
    )
);

-- Totales y serie de una campana.
CREATE INDEX IF NOT EXISTS idx_analytics_daily_campaign_stats_campaign
    ON analytics.daily_campaign_stats (tenant_id, campaign_id, day);

DO $$
BEGIN
    CREATE TRIGGER trg_analytics_daily_campaign_stats_updated
        BEFORE UPDATE ON analytics.daily_campaign_stats
        FOR EACH ROW EXECUTE FUNCTION update_updated_at();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- Agregado por dia, clase y dominio destino: que proveedor rebota o se queja. Solo los
-- mensajes con un dominio de destinatario valido.
CREATE TABLE IF NOT EXISTS analytics.daily_domain_stats (
    tenant_id uuid NOT NULL,
    day date NOT NULL,
    class varchar(20) NOT NULL,
    recipient_domain varchar(253) NOT NULL,
    sent bigint NOT NULL DEFAULT 0,
    delivered bigint NOT NULL DEFAULT 0,
    bounced_hard bigint NOT NULL DEFAULT 0,
    bounced_soft bigint NOT NULL DEFAULT 0,
    complained bigint NOT NULL DEFAULT 0,
    opened_unique bigint NOT NULL DEFAULT 0,
    clicked_unique bigint NOT NULL DEFAULT 0,
    unsubscribed bigint NOT NULL DEFAULT 0,
    failed bigint NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT daily_domain_stats_pkey PRIMARY KEY (tenant_id, day, class, recipient_domain),
    CONSTRAINT daily_domain_stats_class_check CHECK (class IN ('transactional', 'marketing')),
    CONSTRAINT daily_domain_stats_domain_lower CHECK (recipient_domain = lower(recipient_domain)),
    CONSTRAINT daily_domain_stats_counters_check CHECK (
        sent >= 0 AND delivered >= 0 AND bounced_hard >= 0 AND bounced_soft >= 0
        AND complained >= 0 AND opened_unique >= 0 AND clicked_unique >= 0
        AND unsubscribed >= 0 AND failed >= 0
    )
);

DO $$
BEGIN
    CREATE TRIGGER trg_analytics_daily_domain_stats_updated
        BEFORE UPDATE ON analytics.daily_domain_stats
        FOR EACH ROW EXECUTE FUNCTION update_updated_at();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- Campanas vistas en los eventos de campaigns, para listar las campanas con datos sin
-- llamar a su servicio. status_at es el instante del evento que fijo el estado: un evento
-- anterior que llega tarde no lo pisa.
CREATE TABLE IF NOT EXISTS analytics.campaigns_seen (
    campaign_id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    status varchar(32) NOT NULL,
    status_at timestamptz NOT NULL,
    started_at timestamptz,
    completed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_analytics_campaigns_seen_tenant_started
    ON analytics.campaigns_seen (tenant_id, started_at DESC);

DO $$
BEGIN
    CREATE TRIGGER trg_analytics_campaigns_seen_updated
        BEFORE UPDATE ON analytics.campaigns_seen
        FOR EACH ROW EXECUTE FUNCTION update_updated_at();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;
