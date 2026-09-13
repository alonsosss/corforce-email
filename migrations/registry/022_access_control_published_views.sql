-- Schema: access_control | Service: access-control
--
-- Vista publicada para identity, que sella en el access token los roles vigentes del
-- usuario. identity lee esta vista y no las tablas roles y user_roles, que son detalle
-- interno de access-control. Solo roles activos: un rol inactivo no concede nada y no
-- debe llegar al token. Columnas enumeradas; sin descripcion ni datos de auditoria.
--
-- El registro tiene un unico rol de base para el plano de control, por eso no hay GRANT.
-- Una migracion posterior que anada columnas debe redefinir la vista con guarda: re-ejecutar
-- esta sobre una vista ampliada quitaria columnas (42P16).

CREATE OR REPLACE VIEW access_control.v_user_roles AS
    SELECT ur.user_id, r.tenant_id, r.name AS role_name
      FROM access_control.user_roles ur
      JOIN access_control.roles r ON r.id = ur.role_id
     WHERE r.status = 'active';
