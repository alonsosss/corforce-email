-- Schema: mail | Service: mail-directory
--
-- Seguridad del buzon en el webmail (docs/Plan_Webmail_Seguridad.md):
--
--   mailboxes.mfa_enabled: el buzon tiene la verificacion en dos pasos activa. Es lo unico que
--     mail-auth necesita saber: con ella rechaza la contrasena principal en todo protocolo salvo el
--     webmail, que pide el segundo paso. Los grants de mail.mailboxes son por tabla, asi que la
--     columna la leen mail-auth y los motores sin tocarlos.
--   mailbox_mfa: el secreto TOTP del buzon, cifrado con MAIL_ENCRYPTION_KEY (AES-256-GCM con el id
--     del buzon como datos autenticados), los SHA-256 de los codigos de recuperacion que quedan y el
--     ultimo paso de 30 s aceptado, que impide repetir un codigo. Una fila por buzon, solo mientras
--     la verificacion esta activa. El secreto no sale nunca de mail-directory.
--   mail_policy: la politica de correo de la empresa. external_forwarding_allowed dice si un buzon
--     puede reenviar a direcciones de fuera de la empresa; sin fila, permitido (el comportamiento
--     anterior). La cambia el tenant_admin (permiso mailboxes/mail_policy/update).
--
-- Los motores no leen ninguna de las dos tablas nuevas. Al borrar un buzon, mail-directory borra su
-- fila de mailbox_mfa en la misma transaccion. Idempotente y aditiva.

ALTER TABLE mail.mailboxes ADD COLUMN IF NOT EXISTS mfa_enabled boolean NOT NULL DEFAULT false;

CREATE TABLE IF NOT EXISTS mail.mailbox_mfa (
    mailbox_id      uuid PRIMARY KEY,
    tenant_id       uuid NOT NULL,
    secret_enc      bytea NOT NULL,
    recovery_hashes text[] NOT NULL DEFAULT '{}',
    last_step       bigint NOT NULL DEFAULT 0 CHECK (last_step >= 0),
    enabled_at      timestamptz NOT NULL DEFAULT now(),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT mailbox_mfa_recovery_count_check CHECK (cardinality(recovery_hashes) <= 10)
);
CREATE INDEX IF NOT EXISTS idx_mail_mailbox_mfa_tenant ON mail.mailbox_mfa (tenant_id);

CREATE TABLE IF NOT EXISTS mail.mail_policy (
    id                          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id                   uuid NOT NULL UNIQUE,
    external_forwarding_allowed boolean NOT NULL DEFAULT true,
    updated_by                  uuid,
    created_at                  timestamptz NOT NULL DEFAULT now(),
    updated_at                  timestamptz NOT NULL DEFAULT now()
);

DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['mailbox_mfa', 'mail_policy']
    LOOP
        IF NOT EXISTS (
            SELECT 1 FROM pg_trigger
             WHERE tgname = 'trg_mail_' || t || '_updated_at'
               AND tgrelid = ('mail.' || t)::regclass
        ) THEN
            EXECUTE format('CREATE TRIGGER trg_mail_%I_updated_at BEFORE UPDATE ON mail.%I FOR EACH ROW EXECUTE FUNCTION update_updated_at()', t, t);
        END IF;
    END LOOP;
END $$;

ALTER TABLE mail.mailbox_mfa ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON mail.mailbox_mfa;
CREATE POLICY tenant_isolation ON mail.mailbox_mfa FOR ALL TO mail_app
    USING (tenant_id = mail.current_tenant()) WITH CHECK (tenant_id = mail.current_tenant());
DROP POLICY IF EXISTS service_all ON mail.mailbox_mfa;
CREATE POLICY service_all ON mail.mailbox_mfa FOR ALL TO mail_service USING (true) WITH CHECK (true);

ALTER TABLE mail.mail_policy ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON mail.mail_policy;
CREATE POLICY tenant_isolation ON mail.mail_policy FOR ALL TO mail_app
    USING (tenant_id = mail.current_tenant()) WITH CHECK (tenant_id = mail.current_tenant());
DROP POLICY IF EXISTS service_all ON mail.mail_policy;
CREATE POLICY service_all ON mail.mail_policy FOR ALL TO mail_service USING (true) WITH CHECK (true);

GRANT SELECT, INSERT, UPDATE, DELETE ON mail.mailbox_mfa, mail.mail_policy TO mail_app, mail_service;
REVOKE ALL ON mail.mailbox_mfa, mail.mail_policy FROM mail_engine;
