-- Schema: organization | Service: organization
--
-- Vistas publicadas para access-control, que gatea los permisos por los modulos que la
-- empresa tiene contratados. access-control lee estas vistas y no las tablas
-- module_catalog y tenant_modules, que son detalle interno de organization.
--
-- v_module_catalog: cada modulo contratable, si es core (no se deshabilita) y que modulos
-- de permiso agrupa. Sin requires ni label: el lector no los usa.
-- v_tenant_modules: el estado explicito de cada modulo por empresa. La ausencia de filas
-- de una empresa sigue significando "todos los modulos habilitados". Sin updated_at.
--
-- El registro tiene un unico rol de base para el plano de control, por eso no hay GRANT.
-- Una migracion posterior que anada columnas debe redefinir la vista con guarda: re-ejecutar
-- esta sobre una vista ampliada quitaria columnas (42P16).

CREATE OR REPLACE VIEW organization.v_module_catalog AS
    SELECT module, tier, permission_modules
      FROM organization.module_catalog;

CREATE OR REPLACE VIEW organization.v_tenant_modules AS
    SELECT tenant_id, module, enabled
      FROM organization.tenant_modules;
