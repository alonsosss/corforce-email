-- Schema: mail | Service: mail-directory
--
-- Ajustes del buzon que el usuario gestiona desde el webmail (docs/Plan_Webmail_Competitivo.md, 4.2):
--
--   mailbox_signatures: la firma del buzon. El HTML llega saneado por el webmail, que genera tambien
--     la version de texto; una fila por buzon.
--   mailbox_filters: reglas y reenvio, guardados estructurados (jsonb), y el script Sieve que genera
--     mail-directory al guardar (script_data). Dovecot lo lee por la vista v_sieve_user en la ranura
--     sieve_before3, despues del prefiltro del administrador y antes del script personal, de los
--     postfiltros y del archivado de spam de global_sieve_after. El script entero va dentro de "no es
--     spam", asi que una regla nunca reenvia ni archiva spam. No comparte fila con sieve_filters ni con
--     vacation_replies por la misma razon que la respuesta automatica (09_vacation.sql).
--   scheduled_sends: el indice durable de los envios programados. El mensaje ya compuesto espera en la
--     carpeta Scheduled del buzon; aqui su referencia IMAP, la hora y el estado. El trabajador del
--     webmail reclama las filas vencidas de toda la celda con FOR UPDATE SKIP LOCKED y un arriendo.
--
-- Los motores leen solo la vista de reglas, y solo lo que tiene script: nunca las tablas. mail_app las
-- ve por RLS igual que el resto del directorio y mail_service (las rutas internas sin empresa) todas.
-- Al borrar un buzon, mail-directory borra sus filas en la misma transaccion. Idempotente y aditiva.

CREATE TABLE IF NOT EXISTS mail.mailbox_signatures (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid NOT NULL,
    username   text NOT NULL UNIQUE,
    enabled    boolean NOT NULL DEFAULT false,
    html       text NOT NULL DEFAULT '' CHECK (octet_length(html) <= 16384),
    text       text NOT NULL DEFAULT '' CHECK (octet_length(text) <= 16384),
    on_replies boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT mailbox_signatures_enabled_content_check CHECK (NOT enabled OR btrim(html) <> '' OR btrim(text) <> '')
);
CREATE INDEX IF NOT EXISTS idx_mail_mailbox_signatures_tenant ON mail.mailbox_signatures (tenant_id);

CREATE TABLE IF NOT EXISTS mail.mailbox_filters (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid NOT NULL,
    username    text NOT NULL UNIQUE,
    rules       jsonb NOT NULL DEFAULT '[]'::jsonb,
    forwarding  jsonb NOT NULL DEFAULT '{"enabled": false, "addresses": [], "keep_copy": false}'::jsonb,
    -- 1 MiB es sieve_max_script_size de dovecot.conf: un script mayor Dovecot no lo compila.
    script_data text NOT NULL DEFAULT '' CHECK (octet_length(script_data) <= 1048576),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT mailbox_filters_rules_array_check CHECK (jsonb_typeof(rules) = 'array'),
    CONSTRAINT mailbox_filters_forwarding_object_check CHECK (jsonb_typeof(forwarding) = 'object')
);
CREATE INDEX IF NOT EXISTS idx_mail_mailbox_filters_tenant ON mail.mailbox_filters (tenant_id);

CREATE TABLE IF NOT EXISTS mail.scheduled_sends (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    uuid NOT NULL,
    username     text NOT NULL,
    message_id   text NOT NULL CHECK (octet_length(message_id) BETWEEN 1 AND 998),
    folder       text NOT NULL CHECK (octet_length(folder) BETWEEN 1 AND 512),
    uid_validity bigint NOT NULL CHECK (uid_validity BETWEEN 1 AND 4294967295),
    uid          bigint NOT NULL CHECK (uid BETWEEN 1 AND 4294967295),
    send_at      timestamptz NOT NULL,
    subject      text NOT NULL DEFAULT '' CHECK (char_length(subject) <= 998),
    recipients   text[] NOT NULL CHECK (cardinality(recipients) BETWEEN 1 AND 1000),
    status       text NOT NULL DEFAULT 'pending'
                 CHECK (status IN ('pending', 'sending', 'sent', 'failed', 'canceled')),
    attempts     smallint NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    lease_until  timestamptz,
    last_error   text NOT NULL DEFAULT '' CHECK (char_length(last_error) <= 1000),
    sent_at      timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT scheduled_sends_lease_check CHECK (status <> 'sending' OR lease_until IS NOT NULL),
    CONSTRAINT scheduled_sends_sent_check CHECK (status <> 'sent' OR sent_at IS NOT NULL)
);
-- La reclamacion recorre las pendientes vencidas y los arriendos vencidos; el listado es por buzon.
CREATE INDEX IF NOT EXISTS idx_mail_scheduled_sends_due ON mail.scheduled_sends (send_at) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS idx_mail_scheduled_sends_lease ON mail.scheduled_sends (lease_until) WHERE status = 'sending';
CREATE INDEX IF NOT EXISTS idx_mail_scheduled_sends_finished ON mail.scheduled_sends (updated_at)
    WHERE status IN ('sent', 'failed', 'canceled');
CREATE INDEX IF NOT EXISTS idx_mail_scheduled_sends_user ON mail.scheduled_sends (username, send_at);
CREATE INDEX IF NOT EXISTS idx_mail_scheduled_sends_tenant ON mail.scheduled_sends (tenant_id);
-- Un mismo mensaje no se programa dos veces mientras siga activo: un reintento del webmail choca (409).
CREATE UNIQUE INDEX IF NOT EXISTS uq_mail_scheduled_sends_active_message ON mail.scheduled_sends (username, message_id)
    WHERE status IN ('pending', 'sending');

DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['mailbox_signatures', 'mailbox_filters', 'scheduled_sends'] LOOP
        IF NOT EXISTS (
            SELECT 1 FROM pg_trigger
             WHERE tgname = 'trg_mail_' || t || '_updated_at'
               AND tgrelid = ('mail.' || t)::regclass
        ) THEN
            EXECUTE format('CREATE TRIGGER %I BEFORE UPDATE ON mail.%I FOR EACH ROW EXECUTE FUNCTION update_updated_at()',
                           'trg_mail_' || t || '_updated_at', t);
        END IF;
    END LOOP;
END $$;

-- El formato es el de v_sieve_vacation: el diccionario de Dovecot busca el nombre del script y luego
-- sus datos por el id.
CREATE OR REPLACE VIEW mail.v_sieve_user AS
    SELECT md5(script_data) AS id, username, 'active'::text AS script_name, script_data
      FROM mail.mailbox_filters WHERE script_data <> '';

ALTER TABLE mail.mailbox_signatures ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON mail.mailbox_signatures;
CREATE POLICY tenant_isolation ON mail.mailbox_signatures FOR ALL TO mail_app
    USING (tenant_id = mail.current_tenant()) WITH CHECK (tenant_id = mail.current_tenant());
DROP POLICY IF EXISTS service_all ON mail.mailbox_signatures;
CREATE POLICY service_all ON mail.mailbox_signatures FOR ALL TO mail_service USING (true) WITH CHECK (true);

ALTER TABLE mail.mailbox_filters ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON mail.mailbox_filters;
CREATE POLICY tenant_isolation ON mail.mailbox_filters FOR ALL TO mail_app
    USING (tenant_id = mail.current_tenant()) WITH CHECK (tenant_id = mail.current_tenant());
DROP POLICY IF EXISTS service_all ON mail.mailbox_filters;
CREATE POLICY service_all ON mail.mailbox_filters FOR ALL TO mail_service USING (true) WITH CHECK (true);

ALTER TABLE mail.scheduled_sends ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON mail.scheduled_sends;
CREATE POLICY tenant_isolation ON mail.scheduled_sends FOR ALL TO mail_app
    USING (tenant_id = mail.current_tenant()) WITH CHECK (tenant_id = mail.current_tenant());
DROP POLICY IF EXISTS service_all ON mail.scheduled_sends;
CREATE POLICY service_all ON mail.scheduled_sends FOR ALL TO mail_service USING (true) WITH CHECK (true);

GRANT SELECT, INSERT, UPDATE, DELETE ON mail.mailbox_signatures, mail.mailbox_filters, mail.scheduled_sends
    TO mail_app, mail_service;
REVOKE ALL ON mail.mailbox_signatures, mail.mailbox_filters, mail.scheduled_sends FROM mail_engine;
GRANT SELECT ON mail.v_sieve_user TO mail_engine;
