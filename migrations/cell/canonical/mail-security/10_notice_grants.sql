-- Schema: mail_security | Service: mail-security
--
-- Minimo privilegio en el historial de la cuarentena. 01 concede DML sobre TODAS las tablas del
-- esquema a mail_app en cada pasada (y por defecto a las nuevas), asi que el rol de la aplicacion
-- podia borrar o alterar los avisos de cuarentena y la constancia de uso de los enlaces, que son
-- un rastro: nadie escribe en ellos desde el API de administracion.
--
-- Quien escribe de verdad:
-- * quarantine_notices: el barrido de avisos, como duena del pool (OwnerTransactor), y su poda.
--   mail_app solo lee.
-- * quarantine_link_uses: el enlace sin sesion, bajo RLS (TransactRLS): mail_app inserta la
--   constancia de uso y nada mas; UNIQUE (tenant_id, quarantine_id) hace el enlace de un solo uso
--   y un UPDATE o DELETE lo desharia.
--
-- Va despues de 01 y 07 en cada pasada, asi que el REVOKE es el estado final. Idempotente.

GRANT SELECT ON mail_security.quarantine_notices TO mail_app;
REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON mail_security.quarantine_notices FROM mail_app;

GRANT SELECT, INSERT ON mail_security.quarantine_link_uses TO mail_app;
REVOKE UPDATE, DELETE, TRUNCATE ON mail_security.quarantine_link_uses FROM mail_app;
