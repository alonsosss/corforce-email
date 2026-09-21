-- Schema: audit | Service: audit
-- Version del hash de cada fila de audit.audit_logs (docs/adr/0006).
--
-- hash_version 1 es el hash sin clave de la migracion 03: las filas que ya existen lo son, por
-- el DEFAULT, y se verifican con su formula original sin reescribirse. hash_version 2 es un
-- HMAC-SHA256 con clave sobre una serializacion con prefijo de longitud; hash_key_id dice con
-- que llave del anillo se firmo (un identificador derivado de la llave, nunca la llave).
--
-- Aditiva e idempotente. La fila decide con que formula se verifica, y el verificador exige que
-- la version no retroceda: pasar a 1 una fila posterior a una de version 2 se lee como rotura.
-- Aplicar ANTES de desplegar el servicio que escribe estas columnas.

ALTER TABLE audit.audit_logs ADD COLUMN IF NOT EXISTS hash_version smallint NOT NULL DEFAULT 1;
ALTER TABLE audit.audit_logs ADD COLUMN IF NOT EXISTS hash_key_id text;
