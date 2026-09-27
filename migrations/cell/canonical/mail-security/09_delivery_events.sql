-- Schema: mail_security | Service: mail-security
--
-- Registro de entregas del correo corporativo (docs/Plan_Registro_de_Envios.md, seccion 2): lo que
-- Postfix entrega, rebota, aplaza o rechaza, una fila por destinatario y empresa, con el motivo
-- que dio el otro servidor. Lo escribe el lector de la lista POSTFIX_DELIVERY_LOG de mail-security.
--
-- * direction: outbound lo envio un buzon autenticado de la empresa; inbound iba a uno de sus
--   dominios. Un correo entre dos empresas de la celda deja una fila en cada una.
-- * event_key: huella de la linea de registro y la empresa. Su unicidad hace idempotente el
--   lector: una linea procesada dos veces (caida a mitad) no crea otra fila.
-- * Solo el sobre y la respuesta del servidor: ni asunto ni cuerpo.
-- * Se poda por antiguedad (MAIL_DELIVERY_LOG_RETENTION_DAYS).
--
-- Aislamiento como el resto del esquema: tenant_id, RLS para mail_app y service_all para
-- mail_service. Idempotente y aditiva.

CREATE TABLE IF NOT EXISTS mail_security.delivery_events (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     uuid NOT NULL,
    direction     text NOT NULL CHECK (direction IN ('outbound', 'inbound')),
    queue_id      text NOT NULL DEFAULT '',
    message_id    text NOT NULL DEFAULT '',
    sender        text NOT NULL DEFAULT '',
    recipient     text NOT NULL DEFAULT '',
    status        text NOT NULL CHECK (status IN ('sent', 'bounced', 'deferred', 'expired', 'rejected')),
    dsn           text NOT NULL DEFAULT '',
    relay         text NOT NULL DEFAULT '',
    reason        text NOT NULL DEFAULT '',
    delay_seconds numeric(12, 3),
    sasl_username text NOT NULL DEFAULT '',
    client_ip     text NOT NULL DEFAULT '',
    occurred_at   timestamptz NOT NULL,
    event_key     text NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT delivery_events_reason_size CHECK (octet_length(reason) <= 1000),
    CONSTRAINT delivery_events_event_key UNIQUE (tenant_id, event_key)
);

CREATE INDEX IF NOT EXISTS idx_mail_security_delivery_events_tenant_time
    ON mail_security.delivery_events (tenant_id, occurred_at DESC, id);
CREATE INDEX IF NOT EXISTS idx_mail_security_delivery_events_tenant_status
    ON mail_security.delivery_events (tenant_id, status, occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_mail_security_delivery_events_tenant_recipient
    ON mail_security.delivery_events (tenant_id, recipient);
CREATE INDEX IF NOT EXISTS idx_mail_security_delivery_events_tenant_sender
    ON mail_security.delivery_events (tenant_id, sender);
CREATE INDEX IF NOT EXISTS idx_mail_security_delivery_events_occurred
    ON mail_security.delivery_events (occurred_at);

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'mail_app') THEN
        CREATE ROLE mail_app NOLOGIN;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'mail_service') THEN
        CREATE ROLE mail_service NOLOGIN;
    END IF;
END $$;

-- mail_app solo lee. 01 concede DML sobre todas las tablas del esquema (y por defecto a las nuevas)
-- en cada pasada; como esta migracion va siempre despues, el REVOKE deja el registro en solo
-- lectura para la aplicacion: ni una empresa puede borrar su rastro de envios.
GRANT SELECT ON mail_security.delivery_events TO mail_app;
REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON mail_security.delivery_events FROM mail_app;
GRANT SELECT, INSERT, DELETE ON mail_security.delivery_events TO mail_service;

ALTER TABLE mail_security.delivery_events ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON mail_security.delivery_events;
CREATE POLICY tenant_isolation ON mail_security.delivery_events FOR ALL TO mail_app
    USING (tenant_id = mail_security.current_tenant()) WITH CHECK (tenant_id = mail_security.current_tenant());
DROP POLICY IF EXISTS service_all ON mail_security.delivery_events;
CREATE POLICY service_all ON mail_security.delivery_events FOR ALL TO mail_service USING (true) WITH CHECK (true);
