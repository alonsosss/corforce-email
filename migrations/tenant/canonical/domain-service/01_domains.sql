-- Schema: domains | Service: domain-service
--
-- Dominios de correo de la empresa: alta, verificacion DNS (propiedad, MX, SPF, DKIM,
-- DMARC) y custodia de las claves DKIM. La clave privada se guarda cifrada con
-- AES-256-GCM (pkg/crypto.KeyRing, MAIL_ENCRYPTION_KEY) y nunca sale por el API: solo
-- se descifra en memoria para entregarla a mail-security, que la publica en el Redis de
-- los motores. La activacion del dominio en el directorio de la celda la hace
-- mail-directory por API; aqui no se toca el esquema mail.
--
-- Idempotente y aditiva: se puede reejecutar sobre cualquier base de empresa.

CREATE SCHEMA IF NOT EXISTS domains;

-- Trigger de updated_at. Misma definicion que en los demas esquemas de la base de
-- empresa; CREATE OR REPLACE permite que cualquiera la aplique en cualquier orden.
CREATE OR REPLACE FUNCTION update_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- purpose: corporate recibe y envia por la celda; sending solo envia por SES (fase 3);
-- both, ambas cosas. status: pending al alta, verified cuando todos los registros
-- obligatorios responden, failed cuando falta alguno, disabled cuando se retira del
-- servicio sin borrar la fila.
-- dkim_previous_*: la clave anterior durante la ventana de gracia de una rotacion, para
-- que el correo en transito firmado con ella siga verificando; se retira al vencer.
CREATE TABLE IF NOT EXISTS domains.domains (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    domain varchar(253) NOT NULL,
    purpose varchar(20) NOT NULL DEFAULT 'corporate',
    status varchar(20) NOT NULL DEFAULT 'pending',
    verification_token varchar(64) NOT NULL,
    verified_at timestamptz,
    last_checked_at timestamptz,
    dkim_selector varchar(63) NOT NULL,
    dkim_private_key_enc bytea NOT NULL,
    dkim_public_key text NOT NULL,
    dkim_key_bits integer NOT NULL DEFAULT 2048,
    dkim_previous_selector varchar(63),
    dkim_previous_private_key_enc bytea,
    dkim_previous_public_key text,
    dkim_rotated_at timestamptz,
    dmarc_policy varchar(20) NOT NULL DEFAULT 'quarantine',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT domains_tenant_domain_key UNIQUE (tenant_id, domain),
    CONSTRAINT domains_domain_lower CHECK (domain = lower(domain)),
    CONSTRAINT domains_purpose_check CHECK (purpose IN ('corporate', 'sending', 'both')),
    CONSTRAINT domains_status_check CHECK (status IN ('pending', 'verified', 'failed', 'disabled')),
    CONSTRAINT domains_dmarc_policy_check CHECK (dmarc_policy IN ('none', 'quarantine', 'reject')),
    CONSTRAINT domains_dkim_previous_pair CHECK (
        (dkim_previous_selector IS NULL) = (dkim_previous_private_key_enc IS NULL)
        AND (dkim_previous_selector IS NULL) = (dkim_previous_public_key IS NULL)
        AND (dkim_previous_selector IS NULL) = (dkim_rotated_at IS NULL)
    )
);

-- El UNIQUE (tenant_id, domain) ya es el indice de busqueda por nombre; este sirve al
-- barrido de fondo, que selecciona por estado dentro de la empresa.
CREATE INDEX IF NOT EXISTS idx_domains_tenant_status ON domains.domains (tenant_id, status);

DO $$
BEGIN
    CREATE TRIGGER trg_domains_updated
        BEFORE UPDATE ON domains.domains
        FOR EACH ROW EXECUTE FUNCTION update_updated_at();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- Resultado de cada consulta DNS: una fila por registro comprobado y verificacion.
-- expected es el valor que debia publicarse; observed lo que respondio el DNS (vacio si
-- no habia registro); detail explica el fallo en lenguaje de operador.
CREATE TABLE IF NOT EXISTS domains.dns_checks (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    domain_id uuid NOT NULL REFERENCES domains.domains(id) ON DELETE CASCADE,
    checked_at timestamptz NOT NULL DEFAULT now(),
    record varchar(20) NOT NULL,
    expected text NOT NULL,
    observed text NOT NULL DEFAULT '',
    ok boolean NOT NULL,
    detail text NOT NULL DEFAULT '',
    CONSTRAINT dns_checks_record_check CHECK (record IN ('ownership_txt', 'mx', 'spf', 'dkim', 'dkim_previous', 'dmarc'))
);

CREATE INDEX IF NOT EXISTS idx_dns_checks_domain_checked ON domains.dns_checks (domain_id, checked_at DESC);
CREATE INDEX IF NOT EXISTS idx_dns_checks_tenant_checked ON domains.dns_checks (tenant_id, checked_at);
