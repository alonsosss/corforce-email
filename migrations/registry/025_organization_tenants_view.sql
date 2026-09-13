-- Schema: organization | Service: organization
--
-- Vista publicada para identity, que resuelve la empresa del login por slug y pone el
-- nombre de la empresa en el listado de sesiones. identity lee esta vista y no la tabla
-- tenants, que es detalle interno de organization. Sin db_name, cell_id ni settings: el
-- destino de la base y la celda son enrutado del plano de control, no dato del lector.
--
-- El registro tiene un unico rol de base para el plano de control, por eso no hay GRANT.
-- Una migracion posterior que anada columnas debe redefinir la vista con guarda: re-ejecutar
-- esta sobre una vista ampliada quitaria columnas (42P16).

CREATE OR REPLACE VIEW organization.v_tenants AS
    SELECT id AS tenant_id, slug, name, status
      FROM organization.tenants;
