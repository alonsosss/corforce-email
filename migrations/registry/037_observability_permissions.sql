-- Schema: access_control | Service: observability
--
-- Permisos del modulo observability (handlers de /api/v1/observability, docs/adr/0009):
--   logs/read (plataforma): consultar en Loki los registros de un servicio de la plataforma o de los
--     motores de correo, con un filtro de texto literal y una ventana acotada. Los registros mezclan a
--     todas las empresas (una linea de Postfix lleva remitente y destinatario): solo el superadmin, nunca un
--     rol de empresa (018_permission_scope), y el servicio vuelve a exigir el operador. El modulo no se
--     contrata por empresa, asi que no entra en organization.module_catalog. Idempotente.

INSERT INTO access_control.permissions (module, resource, action, description, scope) VALUES
('observability', 'logs', 'read', 'Consultar los registros de los servicios de la plataforma y de los motores de correo', 'platform')
ON CONFLICT (module, resource, action) DO NOTHING;

UPDATE access_control.permissions SET scope = 'platform'
 WHERE module = 'observability' AND resource = 'logs' AND scope <> 'platform';
DELETE FROM access_control.role_permissions rp
 USING access_control.permissions p
 WHERE rp.permission_id = p.id AND p.module = 'observability';
