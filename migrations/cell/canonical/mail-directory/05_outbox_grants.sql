-- Schema: platform | Service: mail-directory
--
-- Los eventos del directorio (mail.domain.*, mail.alias_domain.*, mail.mailbox.*,
-- mail.alias.*) se encolan en la outbox de la celda (platform.event_outbox, de
-- platform/00_outbox.sql) dentro de la MISMA transaccion que el cambio que los origina.
-- Esa transaccion corre bajo el rol mail_app (pkg/db.TransactRLS), que no tenia ningun
-- permiso en el esquema platform: el INSERT de outbox.Enqueue fallaria con permission
-- denied y revertiria la escritura de negocio.
--
-- Solo INSERT: mail_app encola pero no lee ni modifica la outbox, que lleva eventos de
-- todas las empresas de la celda; el rele la vacia como dueno. La tabla no tiene RLS: el
-- aislamiento de lectura lo da la ausencia de SELECT. Idempotente.

GRANT USAGE ON SCHEMA platform TO mail_app;
GRANT INSERT ON platform.event_outbox TO mail_app;
