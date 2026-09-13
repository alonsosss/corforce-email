-- Schema: platform | Service: mail-security
--
-- Los eventos de la cuarentena (mail_security.quarantine.*) se encolan en la outbox de la
-- celda (platform.event_outbox) en la misma transaccion que la fila que los origina. La
-- liberacion corre bajo el rol mail_app (pkg/db.TransactRLS) y necesita poder encolar.
-- Repite la concesion de mail-directory/05 a proposito: las migraciones de este servicio
-- no dependen de que se hayan aplicado las del directorio (ver 01).
--
-- Solo INSERT: mail_app no lee la outbox, que lleva eventos de todas las empresas de la
-- celda. Idempotente.

GRANT USAGE ON SCHEMA platform TO mail_app;
GRANT INSERT ON platform.event_outbox TO mail_app;
