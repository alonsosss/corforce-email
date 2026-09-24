-- Schema: mail | Service: mail-directory
--
-- Recordatorios y respuestas rapidas del webmail (docs/Plan_Webmail_Innovador.md, bloque C2):
--
--   mailbox_reminders: el indice durable de lo que el webmail tiene que hacer a una hora con un mensaje
--     del buzon. snooze (G5): el mensaje espera en la carpeta Snoozed y a su hora vuelve a su carpeta
--     original (return_folder) sin \Seen; si el usuario lo saco antes, la fila se cierra sin tocarlo.
--     follow_up (G6): el mensaje enviado (Message-ID) se comprueba a su hora; si nadie respondio (ningun
--     mensaje del buzon lo cita en In-Reply-To o References) se copia a INBOX con \Flagged y sin \Seen.
--     El mensaje vive en IMAP; aqui su referencia (carpeta, UIDVALIDITY, UID y Message-ID), la hora y
--     el estado. uid_validity y uid faltan solo en un seguimiento de un envio programado, que aun no
--     esta en Enviados y se localiza por su Message-ID. El trabajador del webmail reclama las filas
--     vencidas de toda la celda con FOR UPDATE SKIP LOCKED y un arriendo, como mail.scheduled_sends.
--   mailbox_quick_replies: las respuestas rapidas del buzon (G7). El HTML llega saneado por el webmail,
--     que genera tambien el texto; las variables ({nombre}, {empresa}...) se guardan tal cual y las
--     resuelve la interfaz al insertarlas.
--
-- Los motores no leen ninguna de las dos. mail_app las ve por RLS igual que el resto del directorio y
-- mail_service (las rutas internas sin empresa) todas. Al borrar un buzon, mail-directory borra sus
-- filas en la misma transaccion. Idempotente y aditiva.

CREATE TABLE IF NOT EXISTS mail.mailbox_reminders (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     uuid NOT NULL,
    username      text NOT NULL,
    kind          text NOT NULL CHECK (kind IN ('snooze', 'follow_up')),
    message_id    text NOT NULL DEFAULT '' CHECK (octet_length(message_id) <= 998),
    folder        text NOT NULL CHECK (octet_length(folder) BETWEEN 1 AND 512),
    uid_validity  bigint CHECK (uid_validity BETWEEN 1 AND 4294967295),
    uid           bigint CHECK (uid BETWEEN 1 AND 4294967295),
    return_folder text NOT NULL DEFAULT '' CHECK (octet_length(return_folder) <= 512),
    subject       text NOT NULL DEFAULT '' CHECK (char_length(subject) <= 998),
    addresses     text[] NOT NULL DEFAULT '{}' CHECK (cardinality(addresses) <= 100),
    due_at        timestamptz NOT NULL,
    status        text NOT NULL DEFAULT 'pending'
                  CHECK (status IN ('pending', 'running', 'done', 'failed', 'canceled')),
    result        text NOT NULL DEFAULT ''
                  CHECK (result IN ('', 'returned', 'missing', 'replied', 'reminded')),
    attempts      smallint NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    lease_until   timestamptz,
    last_error    text NOT NULL DEFAULT '' CHECK (char_length(last_error) <= 1000),
    done_at       timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT mailbox_reminders_uid_pair_check CHECK ((uid_validity IS NULL) = (uid IS NULL)),
    CONSTRAINT mailbox_reminders_snooze_check CHECK (
        kind <> 'snooze' OR (uid IS NOT NULL AND return_folder <> '')),
    CONSTRAINT mailbox_reminders_follow_up_check CHECK (
        kind <> 'follow_up' OR (message_id <> '' AND return_folder = '')),
    CONSTRAINT mailbox_reminders_lease_check CHECK (status <> 'running' OR lease_until IS NOT NULL),
    CONSTRAINT mailbox_reminders_done_check CHECK (status <> 'done' OR (done_at IS NOT NULL AND result <> ''))
);
-- La reclamacion recorre las pendientes vencidas y los arriendos vencidos; el listado es por buzon y tipo.
CREATE INDEX IF NOT EXISTS idx_mail_mailbox_reminders_due ON mail.mailbox_reminders (due_at) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS idx_mail_mailbox_reminders_lease ON mail.mailbox_reminders (lease_until) WHERE status = 'running';
CREATE INDEX IF NOT EXISTS idx_mail_mailbox_reminders_finished ON mail.mailbox_reminders (updated_at)
    WHERE status IN ('done', 'failed', 'canceled');
CREATE INDEX IF NOT EXISTS idx_mail_mailbox_reminders_user ON mail.mailbox_reminders (username, kind, due_at);
CREATE INDEX IF NOT EXISTS idx_mail_mailbox_reminders_tenant ON mail.mailbox_reminders (tenant_id);
-- Un mismo mensaje no tiene dos recordatorios activos del mismo tipo: un reintento del webmail choca (409).
CREATE UNIQUE INDEX IF NOT EXISTS uq_mail_mailbox_reminders_active_message
    ON mail.mailbox_reminders (username, kind, message_id)
    WHERE status IN ('pending', 'running') AND message_id <> '';

CREATE TABLE IF NOT EXISTS mail.mailbox_quick_replies (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid NOT NULL,
    username   text NOT NULL,
    name       text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 80),
    html       text NOT NULL DEFAULT '' CHECK (octet_length(html) <= 16384),
    text       text NOT NULL DEFAULT '' CHECK (octet_length(text) <= 16384),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT mailbox_quick_replies_content_check CHECK (btrim(html) <> '' OR btrim(text) <> '')
);
CREATE INDEX IF NOT EXISTS idx_mail_mailbox_quick_replies_tenant ON mail.mailbox_quick_replies (tenant_id);
-- El nombre identifica la respuesta en el selector de la redaccion: no se repite en el buzon.
CREATE UNIQUE INDEX IF NOT EXISTS uq_mail_mailbox_quick_replies_name
    ON mail.mailbox_quick_replies (username, lower(name));

DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['mailbox_reminders', 'mailbox_quick_replies'] LOOP
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

ALTER TABLE mail.mailbox_reminders ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON mail.mailbox_reminders;
CREATE POLICY tenant_isolation ON mail.mailbox_reminders FOR ALL TO mail_app
    USING (tenant_id = mail.current_tenant()) WITH CHECK (tenant_id = mail.current_tenant());
DROP POLICY IF EXISTS service_all ON mail.mailbox_reminders;
CREATE POLICY service_all ON mail.mailbox_reminders FOR ALL TO mail_service USING (true) WITH CHECK (true);

ALTER TABLE mail.mailbox_quick_replies ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON mail.mailbox_quick_replies;
CREATE POLICY tenant_isolation ON mail.mailbox_quick_replies FOR ALL TO mail_app
    USING (tenant_id = mail.current_tenant()) WITH CHECK (tenant_id = mail.current_tenant());
DROP POLICY IF EXISTS service_all ON mail.mailbox_quick_replies;
CREATE POLICY service_all ON mail.mailbox_quick_replies FOR ALL TO mail_service USING (true) WITH CHECK (true);

GRANT SELECT, INSERT, UPDATE, DELETE ON mail.mailbox_reminders, mail.mailbox_quick_replies TO mail_app, mail_service;
REVOKE ALL ON mail.mailbox_reminders, mail.mailbox_quick_replies FROM mail_engine;
