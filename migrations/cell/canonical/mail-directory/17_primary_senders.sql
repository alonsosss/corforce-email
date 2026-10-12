-- Schema: mail | Service: mail-directory
--
-- Remitentes que el usuario fija en la pestana Principal de su bandeja ("Siempre en Principal"):
--
--   mailbox_primary_senders: una fila por buzon y direccion. El webmail la consulta al listar y clasificar
--     (rutas internas de mail-directory) y la traduce a IMAP SEARCH; el directorio solo la guarda y la
--     devuelve, nunca la interpreta. address va en minusculas y la unicidad es por buzon. El tope por
--     buzon lo aplica mail-directory, que tambien borra las filas al borrar el buzon, en la misma
--     transaccion.
--
-- Los motores no la leen: REVOKE a mail_engine. Idempotente y aditiva.

CREATE TABLE IF NOT EXISTS mail.mailbox_primary_senders (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid NOT NULL,
    username   text NOT NULL,
    address    text NOT NULL CHECK (char_length(address) BETWEEN 3 AND 320 AND address = lower(address)),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT mailbox_primary_senders_username_address_key UNIQUE (username, address)
);
CREATE INDEX IF NOT EXISTS idx_mail_mailbox_primary_senders_tenant ON mail.mailbox_primary_senders (tenant_id);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_trigger
         WHERE tgname = 'trg_mail_mailbox_primary_senders_updated_at'
           AND tgrelid = 'mail.mailbox_primary_senders'::regclass
    ) THEN
        CREATE TRIGGER trg_mail_mailbox_primary_senders_updated_at BEFORE UPDATE ON mail.mailbox_primary_senders
            FOR EACH ROW EXECUTE FUNCTION update_updated_at();
    END IF;
END $$;

ALTER TABLE mail.mailbox_primary_senders ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON mail.mailbox_primary_senders;
CREATE POLICY tenant_isolation ON mail.mailbox_primary_senders FOR ALL TO mail_app
    USING (tenant_id = mail.current_tenant()) WITH CHECK (tenant_id = mail.current_tenant());
DROP POLICY IF EXISTS service_all ON mail.mailbox_primary_senders;
CREATE POLICY service_all ON mail.mailbox_primary_senders FOR ALL TO mail_service USING (true) WITH CHECK (true);

GRANT SELECT, INSERT, UPDATE, DELETE ON mail.mailbox_primary_senders TO mail_app, mail_service;
REVOKE ALL ON mail.mailbox_primary_senders FROM mail_engine;
