-- Schema: reputation | Service: reputation
--
-- Reputacion de envio de la empresa por clase (transactional, marketing): envios, rebotes
-- permanentes y quejas por dia; el estado que la evaluacion deriva de sus tasas sobre una
-- ventana movil y su historial; los limites de tasa que fija el superadmin; y los eventos
-- de entrega ya contados. Es lo que consulta la autorizacion previa a cada envio.
--
-- Idempotente y aditiva: se puede reejecutar sobre cualquier base de empresa.

CREATE SCHEMA IF NOT EXISTS reputation;

-- Trigger de updated_at. Misma definicion que en los demas esquemas de la base de
-- empresa; CREATE OR REPLACE permite que cualquiera la aplique en cualquier orden.
CREATE OR REPLACE FUNCTION update_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- Contadores por dia (UTC) y clase. sent cuenta destinatarios, igual que bounced y
-- complained, que llegan uno por destinatario: la tasa compara magnitudes iguales.
-- bounced solo cuenta rebotes permanentes; un rebote transitorio no dice nada de la
-- practica de la empresa.
CREATE TABLE IF NOT EXISTS reputation.daily_stats (
    tenant_id uuid NOT NULL,
    class varchar(20) NOT NULL,
    day date NOT NULL,
    sent bigint NOT NULL DEFAULT 0,
    bounced bigint NOT NULL DEFAULT 0,
    complained bigint NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT reputation_daily_stats_pkey PRIMARY KEY (tenant_id, class, day),
    CONSTRAINT reputation_daily_stats_class_check CHECK (class IN ('transactional', 'marketing')),
    CONSTRAINT reputation_daily_stats_counts_check CHECK (sent >= 0 AND bounced >= 0 AND complained >= 0)
);

DO $$
BEGIN
    CREATE TRIGGER trg_reputation_daily_stats_updated
        BEFORE UPDATE ON reputation.daily_stats
        FOR EACH ROW EXECUTE FUNCTION update_updated_at();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- Estado vigente por clase. bounce_rate y complaint_rate son las tasas del momento del
-- ultimo cambio; las de la ventana actual se calculan al consultar. manual marca un
-- estado fijado por el superadmin: la evaluacion automatica no lo cambia hasta que lo
-- libere. suspended solo existe como estado manual.
CREATE TABLE IF NOT EXISTS reputation.states (
    tenant_id uuid NOT NULL,
    class varchar(20) NOT NULL,
    state varchar(20) NOT NULL DEFAULT 'ok',
    reason text NOT NULL DEFAULT '',
    bounce_rate numeric(9,6) NOT NULL DEFAULT 0,
    complaint_rate numeric(9,6) NOT NULL DEFAULT 0,
    manual boolean NOT NULL DEFAULT false,
    changed_at timestamptz NOT NULL DEFAULT now(),
    changed_by uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT reputation_states_pkey PRIMARY KEY (tenant_id, class),
    CONSTRAINT reputation_states_class_check CHECK (class IN ('transactional', 'marketing')),
    CONSTRAINT reputation_states_state_check CHECK (state IN ('ok', 'warning', 'restricted', 'suspended')),
    CONSTRAINT reputation_states_rates_check CHECK (
        bounce_rate BETWEEN 0 AND 1 AND complaint_rate BETWEEN 0 AND 1
    ),
    CONSTRAINT reputation_states_suspended_manual CHECK (state <> 'suspended' OR manual)
);

DO $$
BEGIN
    CREATE TRIGGER trg_reputation_states_updated
        BEFORE UPDATE ON reputation.states
        FOR EACH ROW EXECUTE FUNCTION update_updated_at();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- Historial de los cambios de estado. manual dice si el cambio lo hizo una persona
-- (suspension o liberacion) y changed_by quien; las tasas son las del momento del cambio.
CREATE TABLE IF NOT EXISTS reputation.state_history (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    class varchar(20) NOT NULL,
    from_state varchar(20) NOT NULL,
    to_state varchar(20) NOT NULL,
    reason text NOT NULL DEFAULT '',
    bounce_rate numeric(9,6) NOT NULL,
    complaint_rate numeric(9,6) NOT NULL,
    manual boolean NOT NULL DEFAULT false,
    changed_by uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT reputation_state_history_class_check CHECK (class IN ('transactional', 'marketing')),
    CONSTRAINT reputation_state_history_states_check CHECK (
        from_state IN ('ok', 'warning', 'restricted', 'suspended')
        AND to_state IN ('ok', 'warning', 'restricted', 'suspended')
    )
);

CREATE INDEX IF NOT EXISTS idx_reputation_state_history_tenant_created
    ON reputation.state_history (tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_reputation_state_history_tenant_class_created
    ON reputation.state_history (tenant_id, class, created_at DESC);

-- El historial explica por que una empresa no pudo enviar: no se corrige ni se borra.
CREATE OR REPLACE FUNCTION reputation.state_history_append_only()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'reputation.state_history es de solo insercion';
END;
$$ LANGUAGE plpgsql;

DO $$
BEGIN
    CREATE TRIGGER trg_reputation_state_history_append_only
        BEFORE UPDATE OR DELETE ON reputation.state_history
        FOR EACH ROW EXECUTE FUNCTION reputation.state_history_append_only();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- Limites de tasa fijados por el superadmin a la empresa. NULL toma el valor por defecto
-- de la configuracion del servicio.
CREATE TABLE IF NOT EXISTS reputation.limit_overrides (
    tenant_id uuid NOT NULL,
    class varchar(20) NOT NULL,
    hourly bigint,
    daily bigint,
    updated_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT reputation_limit_overrides_pkey PRIMARY KEY (tenant_id, class),
    CONSTRAINT reputation_limit_overrides_class_check CHECK (class IN ('transactional', 'marketing')),
    CONSTRAINT reputation_limit_overrides_values_check CHECK (
        (hourly IS NULL OR hourly > 0)
        AND (daily IS NULL OR daily > 0)
        AND (hourly IS NULL OR daily IS NULL OR hourly <= daily)
    )
);

DO $$
BEGIN
    CREATE TRIGGER trg_reputation_limit_overrides_updated
        BEFORE UPDATE ON reputation.limit_overrides
        FOR EACH ROW EXECUTE FUNCTION update_updated_at();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- Eventos de entrega ya contados: una reentrega de JetStream no suma dos veces. Se poda a
-- los 30 dias, muy por encima de la retencion del stream.
CREATE TABLE IF NOT EXISTS reputation.processed_events (
    event_id varchar(200) PRIMARY KEY,
    tenant_id uuid NOT NULL,
    processed_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_reputation_processed_events_processed
    ON reputation.processed_events (processed_at);
