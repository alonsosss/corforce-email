-- Schema: mail | Service: mail-directory
--
-- Bajas de empresa en la celda. La saga de baja de organization pide a mail-directory, por su
-- ruta interna, que de de baja a una empresa en el directorio de la celda: en una transaccion se
-- registra aqui la baja y se apaga todo lo suyo que recibe, reenvia o autentica (dominios,
-- dominios alias, buzones, aliases, contrasenas de aplicacion, relayhosts, transportes, politicas
-- TLS y mapas). Desactiva, no borra: el directorio de la empresa se conserva durante la retencion,
-- como su base de empresa.
--
-- Una fila por empresa dada de baja; ni se actualiza ni se borra. Desde ella el directorio de la
-- empresa no admite escrituras y la activacion de un dominio se rechaza. La escritura y la baja se
-- ordenan con un cerrojo de transaccion por empresa (compartido para escribir, exclusivo para la
-- baja): nada de lo que la baja apaga vuelve a encenderse despues de ella.
--
-- mail_app solo lee la de su empresa (la consulta cada escritura bajo RLS) y la escribe solo
-- mail_service: la ruta interna de la baja corre sin usuario. Los motores no la leen. Idempotente.

CREATE TABLE IF NOT EXISTS mail.tenant_retirements (
    tenant_id  uuid PRIMARY KEY,
    retired_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE mail.tenant_retirements ENABLE ROW LEVEL SECURITY;

-- Los privilegios por defecto del esquema dan DML completo a los dos roles: aqui se acotan.
REVOKE ALL ON mail.tenant_retirements FROM mail_app, mail_service;
GRANT SELECT ON mail.tenant_retirements TO mail_app;
GRANT SELECT, INSERT ON mail.tenant_retirements TO mail_service;

DROP POLICY IF EXISTS tenant_isolation ON mail.tenant_retirements;
CREATE POLICY tenant_isolation ON mail.tenant_retirements FOR SELECT TO mail_app
    USING (tenant_id = mail.current_tenant());
DROP POLICY IF EXISTS service_all ON mail.tenant_retirements;
CREATE POLICY service_all ON mail.tenant_retirements FOR ALL TO mail_service USING (true) WITH CHECK (true);
