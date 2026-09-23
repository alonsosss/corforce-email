-- Schema: analytics | Service: analytics
--
-- Clics por enlace de cada campana, alimentados por transactional.email.clicked. La URL
-- llega normalizada por la ingesta: sin parametros utm_ ni los que llevan la direccion, el
-- contacto o el mensaje del destinatario. Ninguna de las dos tablas guarda la direccion.
--
-- campaign_link_stats es el agregado: clics totales y unicos (mensajes distintos) por URL.
-- No se poda. url = '' acumula los clics de las URL por encima del tope por campana
-- (domain.MaxLinksPerCampaign).
--
-- campaign_link_clicks recuerda que mensaje ya pulso que URL, para que el segundo clic de
-- una persona sume en clics y no en unicos. Se poda con la retencion de message_facts
-- (ANALYTICS_MESSAGE_RETENTION_DAYS).
--
-- url_hash es el sha256 de la URL: la clave no depende de lo larga que sea.
--
-- Idempotente y aditiva.

CREATE TABLE IF NOT EXISTS analytics.campaign_link_stats (
    tenant_id uuid NOT NULL,
    campaign_id uuid NOT NULL,
    url_hash bytea NOT NULL,
    url varchar(2048) NOT NULL,
    clicks bigint NOT NULL DEFAULT 0,
    unique_clicks bigint NOT NULL DEFAULT 0,
    first_clicked_at timestamptz NOT NULL,
    last_clicked_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT campaign_link_stats_pkey PRIMARY KEY (tenant_id, campaign_id, url_hash),
    CONSTRAINT campaign_link_stats_hash_check CHECK (octet_length(url_hash) = 32),
    CONSTRAINT campaign_link_stats_counters_check CHECK (
        clicks >= 0 AND unique_clicks >= 0 AND unique_clicks <= clicks
    )
);

-- La lista de enlaces de una campana, ordenada por clics.
CREATE INDEX IF NOT EXISTS idx_analytics_campaign_link_stats_clicks
    ON analytics.campaign_link_stats (tenant_id, campaign_id, clicks DESC);

DO $$
BEGIN
    CREATE TRIGGER trg_analytics_campaign_link_stats_updated
        BEFORE UPDATE ON analytics.campaign_link_stats
        FOR EACH ROW EXECUTE FUNCTION update_updated_at();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

CREATE TABLE IF NOT EXISTS analytics.campaign_link_clicks (
    message_id uuid NOT NULL,
    url_hash bytea NOT NULL,
    tenant_id uuid NOT NULL,
    campaign_id uuid NOT NULL,
    clicked_at timestamptz NOT NULL,
    CONSTRAINT campaign_link_clicks_pkey PRIMARY KEY (message_id, url_hash),
    CONSTRAINT campaign_link_clicks_hash_check CHECK (octet_length(url_hash) = 32)
);

-- La poda recorre por empresa los clics antiguos.
CREATE INDEX IF NOT EXISTS idx_analytics_campaign_link_clicks_tenant_at
    ON analytics.campaign_link_clicks (tenant_id, clicked_at);

-- El ALTER DEFAULT PRIVILEGES de 02_service_role.sql ya cubre estas tablas cuando las crea el
-- mismo dueno; el GRANT explicito cubre una base cuyo dueno de migraciones hubiera cambiado.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'analytics_service') THEN
        GRANT SELECT, INSERT, UPDATE, DELETE
            ON analytics.campaign_link_stats, analytics.campaign_link_clicks
            TO analytics_service;
    END IF;
END $$;
