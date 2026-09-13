-- Schema: identity | Service: identity
--
-- effective_status en identity.v_user_status: el estado con el que se decide si una cuenta
-- puede tener sesion. El bloqueo por intentos fallidos es temporal (locked_until): una fila
-- en locked cuyo bloqueo ya paso, o sin fecha, cuenta como active. Es la misma regla que
-- aplica identity al iniciar sesion y al renovar (domain.User.EffectiveStatus); sin ella,
-- access-control daba por cerrada con USER_NOT_ACTIVE, y el gateway expulsaba, una cuenta
-- que ya podia volver a entrar.
--
-- Solo anade la columna al final: status sigue siendo el estado guardado. La guarda salta la
-- redefinicion si la columna ya existe, y la 023 salta la suya por la misma columna: volver a
-- aplicarlas no quita columnas (42P16).

DO $mig$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns
                    WHERE table_schema = 'identity' AND table_name = 'v_user_status'
                      AND column_name = 'effective_status') THEN
        EXECUTE $vista$
            CREATE OR REPLACE VIEW identity.v_user_status AS
                SELECT id AS user_id, tenant_id, status, tokens_valid_from,
                       CASE WHEN status = 'locked' AND (locked_until IS NULL OR locked_until <= now())
                            THEN 'active'
                            ELSE status
                       END::varchar(20) AS effective_status
                  FROM identity.users
        $vista$;
    END IF;
END $mig$;
