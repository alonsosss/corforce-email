-- Schema: identity | Service: identity
--
-- Vista publicada para access-control: el estado de cada usuario y su epoch de
-- revocacion. access-control la usa para devolver tokens_valid_from en la politica que
-- consulta el gateway y para resolver que usuarios activos de una empresa tienen un
-- permiso. Sin correo, nombre, hash de contrasena ni secreto MFA: solo lo que el lector
-- necesita.
--
-- El registro tiene un unico rol de base para el plano de control, por eso no hay GRANT.
-- La 027 amplia la vista con effective_status: esta redefinicion se salta si la columna ya
-- existe, porque re-ejecutarla sobre la vista ampliada quitaria columnas (42P16).

DO $mig$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns
                    WHERE table_schema = 'identity' AND table_name = 'v_user_status'
                      AND column_name = 'effective_status') THEN
        EXECUTE $vista$
            CREATE OR REPLACE VIEW identity.v_user_status AS
                SELECT id AS user_id, tenant_id, status, tokens_valid_from
                  FROM identity.users
        $vista$;
    END IF;
END $mig$;
