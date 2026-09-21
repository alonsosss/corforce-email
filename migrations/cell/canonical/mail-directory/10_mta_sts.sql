-- Schema: mail | Service: mail-directory
--
-- Politica MTA-STS (RFC 8461) por dominio de una empresa: el modo con el que otros servidores
-- deben tratar el TLS al entregarle correo. Una fila por dominio; sin fila es lo mismo que none
-- (no se publica nada). El valor inicial al activarla es testing: un enforce con un MX o un
-- certificado que no casan hace que los demas servidores DEJEN de entregar, asi que se llega a el
-- por una accion explicita que la aplicacion valida (dominio activo y MX publicados iguales a los
-- de la plataforma).
--
--   mode       none, testing o enforce.
--   max_age    segundos que un remitente conserva la politica (RFC 8461: hasta un ano).
--   policy_id  identificador de version, el id del TXT _mta-sts.<dominio>; cambia con cada
--              modificacion de la politica. 1 a 32 caracteres alfanumericos (RFC 8461, 3.1).
--
-- Solo lo leen los servicios (mail_app por RLS; mail_service para servir la politica publica sin
-- empresa en la peticion). Ningun motor la consulta: mail_engine no tiene acceso. Idempotente.

CREATE TABLE IF NOT EXISTS mail.mta_sts_policies (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid NOT NULL,
    domain     text NOT NULL UNIQUE,
    mode       text NOT NULL DEFAULT 'testing',
    max_age    integer NOT NULL DEFAULT 86400,
    policy_id  text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT mta_sts_policies_domain_lower CHECK (domain = lower(domain)),
    CONSTRAINT mta_sts_policies_mode_check CHECK (mode IN ('none', 'testing', 'enforce')),
    CONSTRAINT mta_sts_policies_max_age_check CHECK (max_age BETWEEN 1 AND 31557600),
    CONSTRAINT mta_sts_policies_policy_id_check CHECK (policy_id ~ '^[A-Za-z0-9]{1,32}$')
);
CREATE INDEX IF NOT EXISTS idx_mail_mta_sts_policies_tenant ON mail.mta_sts_policies (tenant_id);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_trigger
         WHERE tgname = 'trg_mail_mta_sts_policies_updated_at'
           AND tgrelid = 'mail.mta_sts_policies'::regclass
    ) THEN
        CREATE TRIGGER trg_mail_mta_sts_policies_updated_at BEFORE UPDATE ON mail.mta_sts_policies
            FOR EACH ROW EXECUTE FUNCTION update_updated_at();
    END IF;
END $$;

ALTER TABLE mail.mta_sts_policies ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON mail.mta_sts_policies;
CREATE POLICY tenant_isolation ON mail.mta_sts_policies FOR ALL TO mail_app
    USING (tenant_id = mail.current_tenant()) WITH CHECK (tenant_id = mail.current_tenant());
DROP POLICY IF EXISTS service_all ON mail.mta_sts_policies;
CREATE POLICY service_all ON mail.mta_sts_policies FOR ALL TO mail_service USING (true) WITH CHECK (true);

GRANT SELECT, INSERT, UPDATE, DELETE ON mail.mta_sts_policies TO mail_app, mail_service;
REVOKE ALL ON mail.mta_sts_policies FROM mail_engine;
