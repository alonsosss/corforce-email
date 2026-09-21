-- Schema: audit | Service: audit
-- Cadena de hash de audit.security_events (docs/adr/0006).
--
-- Entran en la cadena los campos INMUTABLES del evento: quien, que, desde donde, el detalle, el
-- riesgo y cuando. El reconocimiento (acknowledged, acknowledged_by, acknowledged_at) es estado
-- de trabajo y cambia a proposito, asi que no forma parte del hash; el rol del servicio solo
-- puede escribir esas tres columnas y los dos hashes.
--
-- A diferencia de audit_logs no hay version 1: la cadena existe solo cuando el servicio tiene
-- clave (AUDIT_HASH_KEY) y firma con ella. Sin clave el evento se escribe sin seq ni hash, como
-- hasta ahora, y no forma parte de la cadena. Por eso seq NO tiene DEFAULT: la asigna el
-- servicio al firmar, y una fila con seq y sin hash es una rotura, no una fila sin cadena.
--
-- Aditiva e idempotente. Corre despues de 04 y 08.

ALTER TABLE audit.security_events ADD COLUMN IF NOT EXISTS seq bigint;
ALTER TABLE audit.security_events ADD COLUMN IF NOT EXISTS prev_hash text;
ALTER TABLE audit.security_events ADD COLUMN IF NOT EXISTS entry_hash text;
ALTER TABLE audit.security_events ADD COLUMN IF NOT EXISTS hash_version smallint;
ALTER TABLE audit.security_events ADD COLUMN IF NOT EXISTS hash_key_id text;

CREATE SEQUENCE IF NOT EXISTS audit.security_events_seq;
ALTER SEQUENCE audit.security_events_seq OWNED BY audit.security_events.seq;

CREATE UNIQUE INDEX IF NOT EXISTS uq_security_events_seq ON audit.security_events (seq) WHERE seq IS NOT NULL;

-- Solo anadir, con la excepcion del reconocimiento. Hasta ahora el rol del servicio podia borrar
-- y reescribir cualquier evento.
REVOKE UPDATE, DELETE, TRUNCATE ON audit.security_events FROM audit_service;
GRANT UPDATE (acknowledged, acknowledged_by, acknowledged_at, prev_hash, entry_hash) ON audit.security_events TO audit_service;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA audit TO audit_service;
