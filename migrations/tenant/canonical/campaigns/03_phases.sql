-- Schema: campaigns | Service: campaigns
--
-- Fases de envio de una campana: prueba A/B (muestra por variante y envio de la ganadora),
-- envio por zona horaria del contacto (un tramo por instante local objetivo) y reenvio a
-- quien no abrio. Cada fase recorre la audiencia con sus propios lotes; el orquestador las
-- procesa en orden y solo pasa a la siguiente cuando la anterior entrego su ultima pagina.
--
-- Idempotente y aditiva: se puede reejecutar sobre cualquier base de empresa.

-- ab_test y resend son la configuracion de la campana ({criterion, sample_percent,
-- decision_window_minutes, variants[]} y {subject, delay_minutes}); ab_winner y
-- ab_decision guardan la decision tomada con los contadores de ese momento. local_send_at es
-- una hora de pared SIN zona: cada contacto la recibe en la suya o en fallback_timezone.
ALTER TABLE campaigns.campaigns ADD COLUMN IF NOT EXISTS ab_test jsonb;
ALTER TABLE campaigns.campaigns ADD COLUMN IF NOT EXISTS ab_winner smallint;
ALTER TABLE campaigns.campaigns ADD COLUMN IF NOT EXISTS ab_decided_at timestamptz;
ALTER TABLE campaigns.campaigns ADD COLUMN IF NOT EXISTS ab_decision jsonb;
ALTER TABLE campaigns.campaigns ADD COLUMN IF NOT EXISTS resend jsonb;
ALTER TABLE campaigns.campaigns ADD COLUMN IF NOT EXISTS local_send_at timestamp;
ALTER TABLE campaigns.campaigns ADD COLUMN IF NOT EXISTS fallback_timezone varchar(64) NOT NULL DEFAULT '';

DO $$
BEGIN
    ALTER TABLE campaigns.campaigns ADD CONSTRAINT campaigns_campaigns_ab_test_check
        CHECK (ab_test IS NULL OR jsonb_typeof(ab_test) = 'object');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

DO $$
BEGIN
    ALTER TABLE campaigns.campaigns ADD CONSTRAINT campaigns_campaigns_resend_check
        CHECK (resend IS NULL OR jsonb_typeof(resend) = 'object');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- La ganadora solo existe con prueba A/B y con su decision registrada.
DO $$
BEGIN
    ALTER TABLE campaigns.campaigns ADD CONSTRAINT campaigns_campaigns_ab_winner_check
        CHECK (ab_winner IS NULL OR (ab_test IS NOT NULL AND ab_winner BETWEEN 0 AND 3
                                     AND ab_decided_at IS NOT NULL AND ab_decision IS NOT NULL));
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- Hora local y zona de respaldo van juntas, y no se combinan con una prueba A/B: la ventana
-- de decision exige que la muestra salga a la vez.
DO $$
BEGIN
    ALTER TABLE campaigns.campaigns ADD CONSTRAINT campaigns_campaigns_local_send_check
        CHECK ((local_send_at IS NULL) = (fallback_timezone = '')
               AND (local_send_at IS NULL OR ab_test IS NULL));
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- key identifica la fase dentro de su campana (main, sample:<n>, winner, zone:<instante>,
-- resend) y hace idempotente su alta. ordinal y slot_at dan el orden de proceso.
-- not_before aplaza la fase: la ganadora hasta que vence la ventana de decision, un tramo
-- de zona hasta su instante, el reenvio hasta que pasa su retraso.
CREATE TABLE IF NOT EXISTS campaigns.phases (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    campaign_id uuid NOT NULL REFERENCES campaigns.campaigns (id) ON DELETE CASCADE,
    kind varchar(20) NOT NULL,
    key varchar(64) NOT NULL,
    ordinal integer NOT NULL,
    variant smallint,
    slot_at timestamptz,
    not_before timestamptz,
    status varchar(20) NOT NULL DEFAULT 'pending',
    targeted integer NOT NULL DEFAULT 0,
    accepted integer NOT NULL DEFAULT 0,
    suppressed integer NOT NULL DEFAULT 0,
    started_at timestamptz,
    completed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT campaigns_phases_campaign_key_key UNIQUE (campaign_id, key),
    CONSTRAINT campaigns_phases_kind_check CHECK (kind IN ('main', 'sample', 'winner', 'zone', 'resend')),
    CONSTRAINT campaigns_phases_status_check CHECK (status IN ('pending', 'done')),
    CONSTRAINT campaigns_phases_variant_check CHECK ((kind = 'sample') = (variant IS NOT NULL)
                                                     AND (variant IS NULL OR variant BETWEEN 0 AND 3)),
    CONSTRAINT campaigns_phases_slot_check CHECK ((kind = 'zone') = (slot_at IS NOT NULL)),
    CONSTRAINT campaigns_phases_done_check CHECK (status <> 'done' OR completed_at IS NOT NULL),
    CONSTRAINT campaigns_phases_counts_check CHECK (targeted >= 0 AND accepted >= 0 AND suppressed >= 0)
);

CREATE INDEX IF NOT EXISTS idx_campaigns_phases_campaign
    ON campaigns.phases (campaign_id, ordinal, slot_at);

DO $$
BEGIN
    CREATE TRIGGER trg_campaigns_phases_updated
        BEFORE UPDATE ON campaigns.phases
        FOR EACH ROW EXECUTE FUNCTION update_updated_at();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- Cada lote pertenece a una fase. Los lotes anteriores a esta migracion quedan en la fase
-- main que se siembra abajo.
ALTER TABLE campaigns.batches ADD COLUMN IF NOT EXISTS phase_id uuid REFERENCES campaigns.phases (id) ON DELETE CASCADE;

-- Quien ya recibio la campana, por ronda: initial (fase principal, muestra, ganadora o
-- tramo de zona) y resend. Solo el id del contacto, sin direccion ni datos personales. Se
-- escribe en la misma transaccion que cierra el lote: la ganadora y los tramos excluyen a
-- quien ya consta, y el reenvio sale una sola vez por contacto.
CREATE TABLE IF NOT EXISTS campaigns.recipients (
    campaign_id uuid NOT NULL REFERENCES campaigns.campaigns (id) ON DELETE CASCADE,
    round varchar(10) NOT NULL,
    contact_id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    phase_id uuid NOT NULL REFERENCES campaigns.phases (id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT campaigns_recipients_pkey PRIMARY KEY (campaign_id, round, contact_id),
    CONSTRAINT campaigns_recipients_round_check CHECK (round IN ('initial', 'resend'))
);

-- message_engagement pasa a tener una fila por mensaje aceptado (no solo por mensaje con
-- interaccion): fase y variante las fija el cierre del lote; contacto y entrega, los
-- eventos de transactional. De ahi salen los resultados por variante y quien no abrio.
ALTER TABLE campaigns.message_engagement ADD COLUMN IF NOT EXISTS contact_id uuid;
ALTER TABLE campaigns.message_engagement ADD COLUMN IF NOT EXISTS phase_kind varchar(20);
ALTER TABLE campaigns.message_engagement ADD COLUMN IF NOT EXISTS variant smallint;
ALTER TABLE campaigns.message_engagement ADD COLUMN IF NOT EXISTS delivered_at timestamptz;

CREATE INDEX IF NOT EXISTS idx_campaigns_message_engagement_contact
    ON campaigns.message_engagement (campaign_id, contact_id);

-- Las campanas que ya tenian lotes reciben su fase main, con los totales de esos lotes, y
-- sus lotes quedan en ella: el orquestador continua donde iba.
INSERT INTO campaigns.phases (tenant_id, campaign_id, kind, key, ordinal, status, targeted, accepted, suppressed, started_at, completed_at)
SELECT c.tenant_id, c.id, 'main', 'main', 0,
       CASE WHEN c.status IN ('completed', 'cancelled', 'failed') THEN 'done' ELSE 'pending' END,
       COALESCE(sum(b.recipients) FILTER (WHERE b.status = 'delivered'), 0),
       COALESCE(sum(b.accepted), 0), COALESCE(sum(b.suppressed), 0),
       COALESCE(c.started_at, min(b.created_at)),
       CASE WHEN c.status IN ('completed', 'cancelled', 'failed') THEN COALESCE(c.completed_at, c.updated_at) END
  FROM campaigns.campaigns c
  JOIN campaigns.batches b ON b.campaign_id = c.id
 GROUP BY c.id
ON CONFLICT (campaign_id, key) DO NOTHING;

UPDATE campaigns.batches b
   SET phase_id = p.id
  FROM campaigns.phases p
 WHERE b.phase_id IS NULL AND p.campaign_id = b.campaign_id AND p.key = 'main';
