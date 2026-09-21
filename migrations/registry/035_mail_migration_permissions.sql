-- Schema: access_control | Service: mail-migration
--
-- Permisos del modulo migration (handlers de /api/v1/mail-migration): migrar un buzon desde otro
-- proveedor por IMAP. Alcance de empresa: llegan al tenant_admin al sembrar el rol y con el
-- resembrado de roles. Idempotente.
--   jobs/read    ver los trabajos de migracion de la empresa y su progreso
--   jobs/create  lanzar la migracion de un buzon (guarda cifrada una credencial de terceros)
--   jobs/cancel  cancelar un trabajo pendiente o en curso
-- El gateway deja pasar un POST a quien tenga cualquier permiso de escritura del modulo; el handler
-- exige despues la accion concreta.

INSERT INTO access_control.permissions (module, resource, action, description, scope) VALUES
('migration', 'jobs', 'read',   'Ver las migraciones de buzones de la empresa y su progreso', 'tenant'),
('migration', 'jobs', 'create', 'Lanzar la migracion de un buzon desde otro proveedor',       'tenant'),
('migration', 'jobs', 'cancel', 'Cancelar una migracion de buzon pendiente o en curso',       'tenant')
ON CONFLICT (module, resource, action) DO NOTHING;

-- La migracion es una funcion del correo corporativo: se contrata con ese modulo del catalogo.
UPDATE organization.module_catalog
   SET permission_modules = permission_modules || '["migration"]'::jsonb
 WHERE module = 'corporate_mail' AND NOT (permission_modules ? 'migration');
