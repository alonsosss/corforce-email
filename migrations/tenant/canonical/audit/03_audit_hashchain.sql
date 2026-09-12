-- Schema: audit | Service: audit
-- Cadena de hash a prueba de manipulacion para audit.audit_logs.
--
-- Cada fila encadena su hash (entry_hash) con el de la fila anterior (prev_hash):
-- alterar el contenido de una fila cambia su hash, y borrar o insertar una fila
-- rompe el eslabon siguiente. Un atacante con acceso a la base ya no puede editar
-- ni borrar el rastro sin dejar una discontinuidad que el verificador
-- (/api/v1/audit/integrity) detecta.
--
-- El servicio no escribe filas sin hash: si estas columnas faltan, la insercion
-- falla en vez de degradarse a una bitacora sin verificar. Idempotente.

ALTER TABLE audit.audit_logs ADD COLUMN IF NOT EXISTS seq bigint;

CREATE SEQUENCE IF NOT EXISTS audit.audit_logs_seq;
ALTER SEQUENCE audit.audit_logs_seq OWNED BY audit.audit_logs.seq;
ALTER TABLE audit.audit_logs ALTER COLUMN seq SET DEFAULT nextval('audit.audit_logs_seq');

ALTER TABLE audit.audit_logs ADD COLUMN IF NOT EXISTS prev_hash text;
ALTER TABLE audit.audit_logs ADD COLUMN IF NOT EXISTS entry_hash text;

-- Orden de la cadena: se recorre por seq ascendente y el ultimo eslabon se
-- localiza por seq descendente.
CREATE INDEX IF NOT EXISTS idx_audit_logs_seq ON audit.audit_logs (seq);
