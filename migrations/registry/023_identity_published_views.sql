-- Schema: identity | Service: identity
--
-- Vista publicada para access-control: el estado de cada usuario y su epoch de
-- revocacion. access-control la usa para devolver tokens_valid_from en la politica que
-- consulta el gateway y para resolver que usuarios activos de una empresa tienen un
-- permiso. Sin correo, nombre, hash de contrasena ni secreto MFA: solo lo que el lector
-- necesita.
--
-- El registro tiene un unico rol de base para el plano de control, por eso no hay GRANT.
-- Una migracion posterior que anada columnas debe redefinir la vista con guarda: re-ejecutar
-- esta sobre una vista ampliada quitaria columnas (42P16).

CREATE OR REPLACE VIEW identity.v_user_status AS
    SELECT id AS user_id, tenant_id, status, tokens_valid_from
      FROM identity.users;
