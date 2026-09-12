-- Schema: suppression | Service: suppression
--
-- Lista de exclusiones de envio de la empresa: direcciones a las que NO se envia correo
-- transaccional ni de marketing. Solo entra lo definitivo (rebote duro, queja, baja,
-- direccion invalida, exclusion manual); los rebotes blandos no se registran aqui.
-- Todo envio consulta esta tabla antes de encolar (POST /internal/suppression/check).
--
-- Idempotente y aditiva: se puede reejecutar sobre cualquier base de empresa.

CREATE SCHEMA IF NOT EXISTS suppression;

-- Trigger de updated_at. Misma definicion que en los demas esquemas de la base de
-- empresa; CREATE OR REPLACE permite que cualquiera la aplique en cualquier orden.
CREATE OR REPLACE FUNCTION update_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- Una fila por direccion y empresa. reason es la causa vigente (la mas grave que se haya
-- registrado: complaint > hard_bounce > unsubscribe > invalid > manual). source dice
-- quien la registro (ses, api, import, campaign:<id>); detail guarda el codigo de rebote
-- o la nota del operador. expires_at solo tiene sentido en una exclusion manual
-- temporal: una direccion que rebota o se queja no vuelve sola a la lista de envio.
CREATE TABLE IF NOT EXISTS suppression.entries (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    email varchar(320) NOT NULL,
    reason varchar(20) NOT NULL,
    source varchar(100) NOT NULL DEFAULT '',
    detail text NOT NULL DEFAULT '',
    message_id uuid,
    campaign_id uuid,
    expires_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT suppression_entries_tenant_email_key UNIQUE (tenant_id, email),
    CONSTRAINT suppression_entries_email_lower CHECK (email = lower(email)),
    CONSTRAINT suppression_entries_reason_check CHECK (
        reason IN ('hard_bounce', 'complaint', 'unsubscribe', 'manual', 'invalid')
    ),
    CONSTRAINT suppression_entries_expires_manual_only CHECK (
        expires_at IS NULL OR reason = 'manual'
    )
);

-- El UNIQUE (tenant_id, email) ya sirve a la consulta previa al envio; este cubre el
-- listado filtrado por causa y las estadisticas.
CREATE INDEX IF NOT EXISTS idx_suppression_entries_tenant_reason
    ON suppression.entries (tenant_id, reason);

DO $$
BEGIN
    CREATE TRIGGER trg_suppression_entries_updated
        BEFORE UPDATE ON suppression.entries
        FOR EACH ROW EXECUTE FUNCTION update_updated_at();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- Rastro de cada carga masiva: cuantas direcciones llegaron, cuantas entraron y cuantas
-- se omitieron (invalidas, repetidas o ya suprimidas).
CREATE TABLE IF NOT EXISTS suppression.imports (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    total integer NOT NULL,
    added integer NOT NULL,
    skipped integer NOT NULL,
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT suppression_imports_counts_check CHECK (
        total >= 0 AND added >= 0 AND skipped >= 0 AND added + skipped = total
    )
);

CREATE INDEX IF NOT EXISTS idx_suppression_imports_tenant_created
    ON suppression.imports (tenant_id, created_at DESC);
