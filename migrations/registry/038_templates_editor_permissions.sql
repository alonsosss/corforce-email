-- Schema: access_control | Service: templates
--
-- Permisos del editor visual de correos (docs/Plan_Editor_Correos.md, seccion 3). Alcance de
-- empresa: llegan al tenant_admin al sembrar el rol y con el resembrado de roles. Idempotente.
--   brand_kit/read    ver el kit de marca de la empresa (logo, colores, tipografias, pie legal)
--   brand_kit/update  cambiarlo; su direccion es la que exige el verificador para publicar marketing
--   assets/read       ver las imagenes subidas para las plantillas
--   assets/create     subir una imagen (analizada con ClamAV antes de guardarse)
--   assets/delete     retirar una imagen del listado; el objeto se conserva para los correos enviados
-- El modulo templates ya se contrata con el catalogo (010_templates_permissions.sql); el gateway
-- deja pasar una escritura a quien tenga cualquier permiso de escritura del modulo y el handler
-- exige despues la accion concreta.

INSERT INTO access_control.permissions (module, resource, action, description, scope) VALUES
('templates', 'brand_kit', 'read',   'Ver el kit de marca de la empresa',                      'tenant'),
('templates', 'brand_kit', 'update', 'Cambiar el kit de marca de la empresa',                  'tenant'),
('templates', 'assets',    'read',   'Ver las imagenes de las plantillas',                     'tenant'),
('templates', 'assets',    'create', 'Subir imagenes para las plantillas',                     'tenant'),
('templates', 'assets',    'delete', 'Retirar imagenes del listado de las plantillas',         'tenant')
ON CONFLICT (module, resource, action) DO NOTHING;
