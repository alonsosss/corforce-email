-- Schema: mail | Service: mail-directory
--
-- Respuesta automatica (vacation de Sieve) de un buzon. Una fila por buzon: el texto, el asunto, cada
-- cuantos dias se repite la respuesta al mismo remitente y una ventana de fechas opcional. La plataforma
-- genera el script Sieve al guardar (script_data) y Dovecot lo lee por la vista v_sieve_vacation, en una
-- tercera ranura despues del filtro del usuario y de los filtros de mail.sieve_filters, para que un
-- mensaje que esos filtros descartan o archivan como spam no reciba respuesta. No comparte fila con
-- sieve_filters a proposito: un script generado y otro escrito a mano no se pueden fundir en uno (los
-- require van al principio), y asi el formulario no toca lo que el administrador haya escrito.
--
-- Los motores leen solo la vista, y solo lo activo: nunca el texto de una respuesta desactivada ni la
-- tabla. mail_app la ve por RLS igual que el resto del directorio. Idempotente.

CREATE TABLE IF NOT EXISTS mail.vacation_replies (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     uuid NOT NULL,
    username      text NOT NULL UNIQUE,
    enabled       boolean NOT NULL DEFAULT false,
    subject       text NOT NULL DEFAULT '' CHECK (char_length(subject) <= 200),
    message       text NOT NULL DEFAULT '' CHECK (char_length(message) <= 8192),
    interval_days smallint NOT NULL DEFAULT 1 CHECK (interval_days BETWEEN 1 AND 30),
    starts_on     date,
    ends_on       date,
    script_data   text NOT NULL DEFAULT '',
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT vacation_replies_window_check CHECK (starts_on IS NULL OR ends_on IS NULL OR ends_on >= starts_on),
    CONSTRAINT vacation_replies_enabled_message_check CHECK (NOT enabled OR btrim(message) <> ''),
    CONSTRAINT vacation_replies_enabled_script_check CHECK (NOT enabled OR script_data <> '')
);
CREATE INDEX IF NOT EXISTS idx_mail_vacation_replies_tenant ON mail.vacation_replies (tenant_id);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_trigger
         WHERE tgname = 'trg_mail_vacation_replies_updated_at'
           AND tgrelid = 'mail.vacation_replies'::regclass
    ) THEN
        CREATE TRIGGER trg_mail_vacation_replies_updated_at BEFORE UPDATE ON mail.vacation_replies
            FOR EACH ROW EXECUTE FUNCTION update_updated_at();
    END IF;
END $$;

-- El formato es el de v_sieve_before y v_sieve_after: el diccionario de Dovecot busca el nombre
-- del script y luego sus datos por el id.
CREATE OR REPLACE VIEW mail.v_sieve_vacation AS
    SELECT md5(script_data) AS id, username, 'active'::text AS script_name, script_data
      FROM mail.vacation_replies WHERE enabled;

ALTER TABLE mail.vacation_replies ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON mail.vacation_replies;
CREATE POLICY tenant_isolation ON mail.vacation_replies FOR ALL TO mail_app
    USING (tenant_id = mail.current_tenant()) WITH CHECK (tenant_id = mail.current_tenant());
DROP POLICY IF EXISTS service_all ON mail.vacation_replies;
CREATE POLICY service_all ON mail.vacation_replies FOR ALL TO mail_service USING (true) WITH CHECK (true);

REVOKE ALL ON mail.vacation_replies FROM mail_engine;
GRANT SELECT ON mail.v_sieve_vacation TO mail_engine;
