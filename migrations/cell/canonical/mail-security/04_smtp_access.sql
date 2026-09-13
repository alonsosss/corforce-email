-- Schema: mail_security | Service: mail-security
--
-- Redes desde las que un buzon puede enviar por SMTP autenticado. Alimenta las claves de
-- Redis que lee Rspamd: SMTP_LIMITED_ACCESS (hash usuario -> 1, multimap) y
-- SMTP_ALLOW_NETS_<usuario> (hash red -> 1, simbolo SMTP_ACCESS de rspamd.local.lua, que
-- puntua 999 un envio desde una red ajena). Un buzon sin filas envia desde cualquier red.
--
-- network es cidr: Postgres valida y normaliza. SMTP_ACCESS solo compara prefijos IPv4 de
-- /8 a /32 e IPv6 de /32 a /128; una red mas ancha no casaria nunca y dejaria al buzon
-- sin poder enviar, por eso el CHECK. Aislamiento como el resto del esquema: tenant_id y
-- RLS para mail_app. Idempotente y aditiva.

CREATE TABLE IF NOT EXISTS mail_security.smtp_access_networks (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid NOT NULL,
    username   text NOT NULL,
    network    cidr NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT smtp_access_networks_username_lower CHECK (username = lower(username)),
    CONSTRAINT smtp_access_networks_prefix CHECK (
        (family(network) = 4 AND masklen(network) BETWEEN 8 AND 32)
        OR (family(network) = 6 AND masklen(network) BETWEEN 32 AND 128)),
    UNIQUE (username, network)
);
CREATE INDEX IF NOT EXISTS idx_mail_security_smtp_access_networks_tenant
    ON mail_security.smtp_access_networks (tenant_id, username);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_trigger
         WHERE tgname = 'trg_mail_security_smtp_access_networks_updated_at'
           AND tgrelid = 'mail_security.smtp_access_networks'::regclass
    ) THEN
        CREATE TRIGGER trg_mail_security_smtp_access_networks_updated_at BEFORE UPDATE ON mail_security.smtp_access_networks
            FOR EACH ROW EXECUTE FUNCTION update_updated_at();
    END IF;
END $$;

GRANT SELECT, INSERT, UPDATE, DELETE ON mail_security.smtp_access_networks TO mail_app;
ALTER TABLE mail_security.smtp_access_networks ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON mail_security.smtp_access_networks;
CREATE POLICY tenant_isolation ON mail_security.smtp_access_networks FOR ALL TO mail_app
    USING (tenant_id = mail_security.current_tenant())
    WITH CHECK (tenant_id = mail_security.current_tenant());
