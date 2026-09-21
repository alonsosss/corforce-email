-- Schema: audit | Service: audit
--
-- El rastro es de solo anadir tambien para el rol del servicio. Hasta ahora esto solo lo
-- garantizaba el codigo del repositorio (no expone UPDATE ni DELETE): el rol audit_service
-- tenia los cuatro permisos DML sobre toda tabla del esquema, asi que un fallo del servicio,
-- o quien le robe la credencial, podia borrar filas o reescribirlas y luego recalcular la
-- cadena de hash a su gusto.
--
-- El servicio solo inserta filas y despues fija prev_hash y entry_hash en la misma
-- transaccion (audit.audit_logs); nunca borra ni modifica el contenido. Ese UPDATE
-- se limita por columna. data_change_records se inserta y se lee, nada mas.
--
-- Idempotente: REVOKE y GRANT se pueden repetir. Corre despues de 04, que da los permisos
-- amplios: el orden numerico lo garantiza.

REVOKE UPDATE, DELETE, TRUNCATE ON audit.audit_logs FROM audit_service;
GRANT UPDATE (prev_hash, entry_hash) ON audit.audit_logs TO audit_service;

REVOKE UPDATE, DELETE, TRUNCATE ON audit.data_change_records FROM audit_service;
