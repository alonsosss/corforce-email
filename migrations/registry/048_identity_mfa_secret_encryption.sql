-- Schema: identity | Service: identity
-- Segundo factor de la consola de plataforma (docs/Usuarios_Roles_y_Acceso.md):
--
--   mfa_secret_enc: el secreto TOTP cifrado con MAIL_ENCRYPTION_KEY (AES-256-GCM, con
--     "identity-mfa:<id del usuario>" como datos autenticados: copiado a otra fila no se abre).
--     identity escribe solo esta columna y deja mfa_secret a NULL; al arrancar, y despues cada
--     hora, cifra los secretos en claro que queden y re-cifra bajo la llave activa lo que solo
--     abre una retirada (MAIL_ENCRYPTION_KEYS_OLD).
--   mfa_last_step: el ultimo paso de 30 s aceptado. Un codigo solo vale si su paso es mayor, en
--     una sola sentencia condicional: el mismo codigo no se usa dos veces.
--
-- mfa_secret se conserva vacio en vez de borrarse: las migraciones son solo aditivas, y una
-- replica anterior que siga en pie durante un despliegue aun la escribe; el barrido la cifra.
-- Idempotente y aditiva.

ALTER TABLE identity.users ADD COLUMN IF NOT EXISTS mfa_secret_enc bytea;
ALTER TABLE identity.users ADD COLUMN IF NOT EXISTS mfa_last_step bigint NOT NULL DEFAULT 0;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'users_mfa_last_step_check') THEN
        ALTER TABLE identity.users ADD CONSTRAINT users_mfa_last_step_check CHECK (mfa_last_step >= 0);
    END IF;
END $$;
