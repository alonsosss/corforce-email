-- Schema: identity | Service: identity
--
-- Bloqueo por intentos que no delata la cuenta. Un correo sin cuenta cuenta sus inicios fallidos
-- con la misma regla, el mismo umbral y el mismo plazo que una cuenta, y responde el mismo 403
-- ACCOUNT_LOCKED al llegar al umbral. Su contador vive aqui, nunca en identity.users, con la
-- clave resumida (SHA-256 del ambito y del correo) para no guardar lo que alguien probo.
--
-- Los fallos se olvidan pasadas 24 horas (domain.FailedLoginWindow) desde el ultimo fallo
-- contado y desde el final del ultimo bloqueo, en una cuenta y en un correo sin cuenta: por eso
-- la columna last_failed_login_at, y por eso el contador de un correo sin cuenta se puede borrar
-- pasado ese plazo sin cambiar ninguna respuesta. Una cuenta con fallos anteriores a esta
-- migracion (sin fecha de ultimo fallo) los conserva hasta su siguiente fallo.

ALTER TABLE identity.users ADD COLUMN IF NOT EXISTS last_failed_login_at timestamptz;

CREATE TABLE IF NOT EXISTS identity.unknown_login_failures (
    subject_hash    varchar(64) PRIMARY KEY,
    failed_attempts integer NOT NULL CHECK (failed_attempts > 0),
    last_failed_at  timestamptz NOT NULL,
    locked_until    timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

-- La poda recorre los contadores olvidados por esta misma expresion.
CREATE INDEX IF NOT EXISTS idx_identity_unknown_login_failures_reference
    ON identity.unknown_login_failures ((GREATEST(last_failed_at, locked_until)));

DO $$
BEGIN
    CREATE TRIGGER trg_identity_unknown_login_failures_updated_at
        BEFORE UPDATE ON identity.unknown_login_failures
        FOR EACH ROW EXECUTE FUNCTION update_updated_at();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;
