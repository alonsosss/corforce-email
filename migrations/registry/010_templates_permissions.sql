-- Schema: access_control | Service: templates
--
-- Permisos del modulo templates. El modulo se contrata con el catalogo 'transactional'
-- (organization.module_catalog.permission_modules); aqui solo se declaran las acciones
-- que el handler exige ademas del gateo por modulo del gateway. Idempotente por UNIQUE
-- (module, resource, action).

INSERT INTO access_control.permissions (module, resource, action, description) VALUES
('templates', 'templates', 'read',    'Ver plantillas de correo y sus versiones'),
('templates', 'templates', 'create',  'Crear plantillas y versiones'),
('templates', 'templates', 'update',  'Modificar nombre, descripcion y estado de una plantilla'),
('templates', 'templates', 'delete',  'Borrar plantillas archivadas'),
('templates', 'templates', 'publish', 'Publicar una version de plantilla'),
('templates', 'templates', 'render',  'Renderizar o previsualizar una plantilla')
ON CONFLICT (module, resource, action) DO NOTHING;
