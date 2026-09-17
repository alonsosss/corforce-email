-- Schema: domains | Service: domain-service
--
-- Publicacion automatica del DNS de los dominios en el proveedor DNS de la empresa.
--
-- domains.dns_providers: la conexion de la empresa con un proveedor (hoy solo cloudflare), una
-- por proveedor. api_token_enc es el token de API cifrado con AES-256-GCM (pkg/crypto.KeyRing,
-- MAIL_ENCRYPTION_KEY, con rotacion de llaves), igual que la clave privada DKIM: nunca se guarda
-- en claro ni sale por el API, los eventos o la auditoria. api_token_hint son sus ultimos
-- caracteres, para que quien lo conecto lo reconozca. zones y zones_visible son las zonas que el
-- token veia en la ultima validacion (conectar, cambiar de modo o publicar). Desconectar borra la
-- fila, y con ella el token.
--
-- domains.domains.dns_mode: como se publica el DNS del dominio. manual (por defecto, lo de
-- siempre: el cliente publica los registros) o el proveedor conectado, que publica la plataforma.
-- dns_published_at: la ultima publicacion automatica completada.
--
-- Idempotente y aditiva: se puede reejecutar sobre cualquier base de empresa.

CREATE TABLE IF NOT EXISTS domains.dns_providers (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    provider varchar(32) NOT NULL,
    api_token_enc bytea NOT NULL,
    api_token_hint varchar(8) NOT NULL DEFAULT '',
    zones text[] NOT NULL DEFAULT '{}',
    zones_visible integer NOT NULL DEFAULT 0,
    connected_by uuid,
    connected_at timestamptz NOT NULL DEFAULT now(),
    last_validated_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT dns_providers_tenant_provider_key UNIQUE (tenant_id, provider),
    CONSTRAINT dns_providers_provider_check CHECK (provider IN ('cloudflare')),
    CONSTRAINT dns_providers_zones_visible_check CHECK (zones_visible >= 0)
);

DO $$
BEGIN
    CREATE TRIGGER trg_dns_providers_updated
        BEFORE UPDATE ON domains.dns_providers
        FOR EACH ROW EXECUTE FUNCTION update_updated_at();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

ALTER TABLE domains.domains ADD COLUMN IF NOT EXISTS dns_mode varchar(32) NOT NULL DEFAULT 'manual';
ALTER TABLE domains.domains ADD COLUMN IF NOT EXISTS dns_published_at timestamptz;

ALTER TABLE domains.domains DROP CONSTRAINT IF EXISTS domains_dns_mode_check;
ALTER TABLE domains.domains ADD CONSTRAINT domains_dns_mode_check CHECK (dns_mode IN ('manual', 'cloudflare'));

-- Desconectar un proveedor devuelve a manual los dominios de la empresa que lo usaban.
CREATE INDEX IF NOT EXISTS idx_domains_dns_mode ON domains.domains (tenant_id, dns_mode)
    WHERE dns_mode <> 'manual';
