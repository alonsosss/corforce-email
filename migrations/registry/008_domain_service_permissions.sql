-- Schema: access_control | Service: domain-service
--
-- Permisos del modulo domains que solo domain-service ejerce: verificar un dominio
-- contra el DNS y rotar su clave DKIM. Las acciones basicas (read, create, update,
-- delete) sobre domains/domains las siembra mail-directory en su propia migracion; se
-- repiten aqui con ON CONFLICT DO NOTHING para que el modulo quede completo aunque esta
-- migracion corra antes o esa aun no exista. Idempotente por UNIQUE (module, resource,
-- action).

INSERT INTO access_control.permissions (module, resource, action, description) VALUES
('domains', 'domains', 'read',        'Ver dominios de correo'),
('domains', 'domains', 'create',      'Dar de alta dominios de correo'),
('domains', 'domains', 'update',      'Modificar dominios de correo'),
('domains', 'domains', 'delete',      'Dar de baja dominios de correo'),
('domains', 'domains', 'verify',      'Verificar los registros DNS de un dominio'),
('domains', 'domains', 'rotate_dkim', 'Rotar la clave DKIM de un dominio')
ON CONFLICT (module, resource, action) DO NOTHING;
