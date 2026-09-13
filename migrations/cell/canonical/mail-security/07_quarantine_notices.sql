-- Schema: mail_security | Service: mail-security
--
-- Aviso de cuarentena al buzon y enlaces sin sesion para liberar o descartar un mensaje.
--
-- quarantine_notices: un registro por aviso intentado (sent, suppressed, rejected,
-- skipped), escrito en la MISMA transaccion que marca notified en los mensajes que lista
-- (quarantine_ids). idempotency_key es la que viaja a transactional
-- (quarantine-notice:<buzon>:<id mas reciente>); su unicidad por empresa evita un segundo
-- registro cuando un barrido repite un aviso ya aceptado.
--
-- quarantine_link_uses: constancia del uso de un enlace firmado (accion, ip real que puso
-- el gateway, user agent). UNIQUE (tenant_id, quarantine_id) es lo que hace los enlaces de
-- un solo uso: el primero que se usa, de cualquiera de las dos acciones, consume los dos.
-- Sin clave foranea a quarantine a proposito: la fila del mensaje se borra al liberarlo o
-- descartarlo y la constancia se queda.
--
-- Ambas se podan con el max_age_days de su empresa. Aislamiento como el resto del esquema:
-- tenant_id, RLS para mail_app y service_all para mail_service (06). Autosuficiente,
-- idempotente y aditiva.

CREATE TABLE IF NOT EXISTS mail_security.quarantine_notices (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       uuid NOT NULL,
    rcpt            text NOT NULL,
    idempotency_key text NOT NULL,
    status          text NOT NULL CHECK (status IN ('sent', 'suppressed', 'rejected', 'skipped')),
    message_id      uuid,
    error_code      text NOT NULL DEFAULT '',
    quarantine_ids  uuid[] NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT quarantine_notices_rejected_code CHECK (status <> 'rejected' OR error_code <> ''),
    CONSTRAINT quarantine_notices_ids CHECK (cardinality(quarantine_ids) > 0),
    UNIQUE (tenant_id, idempotency_key)
);
CREATE INDEX IF NOT EXISTS idx_mail_security_quarantine_notices_tenant_rcpt
    ON mail_security.quarantine_notices (tenant_id, rcpt, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_mail_security_quarantine_notices_created
    ON mail_security.quarantine_notices (created_at);

CREATE TABLE IF NOT EXISTS mail_security.quarantine_link_uses (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     uuid NOT NULL,
    quarantine_id uuid NOT NULL,
    action        text NOT NULL CHECK (action IN ('release', 'discard')),
    rcpt          text NOT NULL,
    client_ip     inet,
    user_agent    text NOT NULL DEFAULT '',
    used_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, quarantine_id)
);
CREATE INDEX IF NOT EXISTS idx_mail_security_quarantine_link_uses_used
    ON mail_security.quarantine_link_uses (used_at);

-- Lo pendiente de aviso por empresa y buzon, del mas reciente al mas antiguo. Parcial: una
-- vez avisada la fila sale del indice.
CREATE INDEX IF NOT EXISTS idx_mail_security_quarantine_pending_notice
    ON mail_security.quarantine (tenant_id, rcpt, created_at DESC, id DESC) WHERE notified = false;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'mail_app') THEN
        CREATE ROLE mail_app NOLOGIN;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'mail_service') THEN
        CREATE ROLE mail_service NOLOGIN;
    END IF;
END $$;

GRANT SELECT, INSERT ON mail_security.quarantine_notices, mail_security.quarantine_link_uses TO mail_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON mail_security.quarantine_notices, mail_security.quarantine_link_uses TO mail_service;

DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['quarantine_notices', 'quarantine_link_uses']
    LOOP
        EXECUTE format('ALTER TABLE mail_security.%I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON mail_security.%I', t);
        EXECUTE format(
            'CREATE POLICY tenant_isolation ON mail_security.%I FOR ALL TO mail_app '
            'USING (tenant_id = mail_security.current_tenant()) WITH CHECK (tenant_id = mail_security.current_tenant())', t);
        EXECUTE format('DROP POLICY IF EXISTS service_all ON mail_security.%I', t);
        EXECUTE format(
            'CREATE POLICY service_all ON mail_security.%I FOR ALL TO mail_service USING (true) WITH CHECK (true)', t);
    END LOOP;
END $$;
