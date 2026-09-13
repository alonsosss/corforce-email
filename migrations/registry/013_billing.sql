-- Schema: billing | Service: billing
--
-- Planes con limites por recurso, suscripcion de cada empresa y contadores de consumo. Vive
-- en el registro porque es plano de control: decide que puede hacer cada empresa, no guarda
-- su negocio. tenant_id referencia organization.tenants solo por valor (sin claves foraneas
-- entre esquemas). Idempotente y aditiva; update_updated_at() la crea 001_organization.sql.

CREATE SCHEMA IF NOT EXISTS billing;

CREATE TABLE IF NOT EXISTS billing.plans (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    code           text NOT NULL UNIQUE CHECK (code ~ '^[a-z][a-z0-9_-]{1,39}$'),
    name           text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 120),
    description    text NOT NULL DEFAULT '' CHECK (length(description) <= 1000),
    currency       text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    base_price     numeric(15,2) NOT NULL CHECK (base_price >= 0),
    billing_period text NOT NULL CHECK (billing_period IN ('monthly', 'yearly')),
    -- Un plan retirado conserva a sus suscriptores pero no se asigna a nadie mas.
    status         text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'retired')),
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_billing_plans_status ON billing.plans (status);

CREATE OR REPLACE TRIGGER trg_billing_plans_updated_at BEFORE UPDATE ON billing.plans
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();

-- included = -1 es ilimitado. hard_limit deniega al superar lo incluido; un limite blando
-- lo permite y lo cuenta como excedente, que es lo unico que puede llevar precio unitario.
CREATE TABLE IF NOT EXISTS billing.plan_limits (
    plan_id            uuid NOT NULL REFERENCES billing.plans (id),
    resource           text NOT NULL CHECK (resource IN ('users', 'domains', 'mailboxes', 'storage_bytes',
                                                         'contacts', 'transactional_messages', 'marketing_messages')),
    included           bigint NOT NULL CHECK (included >= -1),
    hard_limit         boolean NOT NULL,
    overage_unit_price numeric(15,6) CHECK (overage_unit_price >= 0),
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (plan_id, resource),
    CHECK (overage_unit_price IS NULL OR (NOT hard_limit AND included >= 0))
);

CREATE OR REPLACE TRIGGER trg_billing_plan_limits_updated_at BEFORE UPDATE ON billing.plan_limits
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();

-- Una suscripcion por empresa. Los periodos son dias de calendario UTC, [inicio, fin), y
-- anchor_day fija el dia del mes en que empiezan: con ancla 31, 31 ene -> 28/29 feb -> 31 mar.
CREATE TABLE IF NOT EXISTS billing.subscriptions (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id            uuid NOT NULL UNIQUE,
    plan_id              uuid NOT NULL REFERENCES billing.plans (id),
    status               text NOT NULL CHECK (status IN ('trialing', 'active', 'past_due', 'suspended', 'cancelled')),
    current_period_start date NOT NULL,
    current_period_end   date NOT NULL,
    anchor_day           smallint NOT NULL CHECK (anchor_day BETWEEN 1 AND 31),
    trial_ends_at        timestamptz,
    cancel_at            timestamptz,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now(),
    CHECK (current_period_end > current_period_start),
    CHECK (status <> 'trialing' OR trial_ends_at IS NOT NULL)
);

CREATE INDEX IF NOT EXISTS idx_billing_subscriptions_plan ON billing.subscriptions (plan_id);
CREATE INDEX IF NOT EXISTS idx_billing_subscriptions_status ON billing.subscriptions (status);
CREATE INDEX IF NOT EXISTS idx_billing_subscriptions_period_end
    ON billing.subscriptions (current_period_end) WHERE status <> 'cancelled';

CREATE OR REPLACE TRIGGER trg_billing_subscriptions_updated_at BEFORE UPDATE ON billing.subscriptions
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();

-- Contadores de consumo. Los recursos de stock (lo que existe) tienen un unico contador
-- vivo en period_start = 1970-01-01; los de flujo (mensajes) uno por periodo de la
-- suscripcion, que empieza en cero. limit_reached_period guarda el periodo de suscripcion
-- en que se aviso billing.limit.reached, para avisar una vez por periodo y recurso.
CREATE TABLE IF NOT EXISTS billing.usage_counters (
    tenant_id            uuid NOT NULL,
    resource             text NOT NULL CHECK (resource IN ('users', 'domains', 'mailboxes', 'storage_bytes',
                                                           'contacts', 'transactional_messages', 'marketing_messages')),
    period_start         date NOT NULL,
    quantity             bigint NOT NULL DEFAULT 0 CHECK (quantity >= 0),
    limit_reached_period date,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, resource, period_start)
);

CREATE OR REPLACE TRIGGER trg_billing_usage_counters_updated_at BEFORE UPDATE ON billing.usage_counters
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();

-- Deduplicacion del consumo: el id del evento se inserta en la misma transaccion que el
-- contador, asi que una reentrega de JetStream no cuenta dos veces. Se poda por antiguedad.
CREATE TABLE IF NOT EXISTS billing.processed_events (
    event_id     text PRIMARY KEY CHECK (length(event_id) BETWEEN 1 AND 200),
    subject      text NOT NULL,
    processed_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_billing_processed_events_at ON billing.processed_events (processed_at);

-- Objetos de stock que informan varias fuentes: un dominio lo da de alta domain-service y,
-- al verificarse, mail-directory lo crea en el directorio. El contador sube con la primera
-- fuente del objeto y baja con la ultima, de modo que un dominio cuenta una sola vez.
CREATE TABLE IF NOT EXISTS billing.stock_items (
    tenant_id  uuid NOT NULL,
    resource   text NOT NULL CHECK (resource IN ('users', 'domains', 'mailboxes', 'storage_bytes', 'contacts')),
    item_key   text NOT NULL CHECK (length(item_key) BETWEEN 1 AND 320),
    source     text NOT NULL CHECK (length(source) BETWEEN 1 AND 64),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, resource, item_key, source)
);
