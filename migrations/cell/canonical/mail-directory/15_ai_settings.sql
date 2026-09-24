-- Schema: mail | Service: mail-directory
--
-- Ajuste por empresa del asistente del webmail (docs/Plan_Webmail_Innovador.md G13, decision 9;
-- docs/adr/0014-asistente-del-webmail-con-claude.md):
--
--   assistant_settings: una fila por empresa con el interruptor del asistente. Sin fila, apagado. Lo
--     cambia el tenant_admin desde el panel (permiso mailboxes/assistant_settings/update) y lo lee el
--     webmail en cada uso, con una cache corta, por la ruta interna de mail-directory. updated_by es el
--     usuario de la plataforma que hizo el ultimo cambio y enabled_at el instante de la ultima
--     activacion, que es cuando se acepto el aviso de tratamiento de datos. No guarda contenido de
--     correo ni nada que se envie al proveedor.
--
-- Vive en la celda y no en la base de la empresa porque quien lo consulta en cada uso es el webmail,
-- un servicio de celda sin base propia que ya habla con mail-directory. Los motores no lo leen.
-- Idempotente y aditiva.

CREATE TABLE IF NOT EXISTS mail.assistant_settings (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid NOT NULL UNIQUE,
    enabled    boolean NOT NULL DEFAULT false,
    updated_by uuid,
    enabled_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT assistant_settings_enabled_at_check CHECK (NOT enabled OR enabled_at IS NOT NULL)
);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_trigger
         WHERE tgname = 'trg_mail_assistant_settings_updated_at'
           AND tgrelid = 'mail.assistant_settings'::regclass
    ) THEN
        CREATE TRIGGER trg_mail_assistant_settings_updated_at BEFORE UPDATE ON mail.assistant_settings
            FOR EACH ROW EXECUTE FUNCTION update_updated_at();
    END IF;
END $$;

ALTER TABLE mail.assistant_settings ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON mail.assistant_settings;
CREATE POLICY tenant_isolation ON mail.assistant_settings FOR ALL TO mail_app
    USING (tenant_id = mail.current_tenant()) WITH CHECK (tenant_id = mail.current_tenant());
DROP POLICY IF EXISTS service_all ON mail.assistant_settings;
CREATE POLICY service_all ON mail.assistant_settings FOR ALL TO mail_service USING (true) WITH CHECK (true);

GRANT SELECT, INSERT, UPDATE, DELETE ON mail.assistant_settings TO mail_app, mail_service;
REVOKE ALL ON mail.assistant_settings FROM mail_engine;
