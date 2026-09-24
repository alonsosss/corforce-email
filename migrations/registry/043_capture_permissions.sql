-- Schema: access_control | Service: contacts, templates
--
-- Permisos de la captacion (docs/Plan_Marketing_Avanzado.md, 2-F). Alcance de empresa: llegan al
-- tenant_admin al sembrar el rol y con el resembrado de roles. Idempotente.
--   contacts/forms/read       ver los formularios de suscripcion, su codigo para incrustar y sus estadisticas
--   contacts/forms/create     crear un formulario
--   contacts/forms/update     cambiar su definicion, sus dominios permitidos o su estado
--   contacts/forms/delete     borrarlo (sus estadisticas se van con el)
--   templates/pages/read      ver las paginas de aterrizaje y sus versiones
--   templates/pages/create    crear una pagina y guardar versiones nuevas desde el editor
--   templates/pages/update    cambiar nombre, slug, indexacion o archivarla
--   templates/pages/delete    borrar una pagina archivada
--   templates/pages/publish   publicar una version en la URL publica de la empresa o retirarla
-- La ruta publica del envio de un formulario y la de la pagina publicada no llevan sesion: su
-- proteccion es del servicio (docs/Operacion_Despliegue.md, «Captacion»).

INSERT INTO access_control.permissions (module, resource, action, description, scope) VALUES
('contacts',  'forms', 'read',    'Ver los formularios de suscripcion y sus estadisticas',       'tenant'),
('contacts',  'forms', 'create',  'Crear formularios de suscripcion',                            'tenant'),
('contacts',  'forms', 'update',  'Editar formularios de suscripcion',                           'tenant'),
('contacts',  'forms', 'delete',  'Borrar formularios de suscripcion',                           'tenant'),
('templates', 'pages', 'read',    'Ver las paginas de aterrizaje',                               'tenant'),
('templates', 'pages', 'create',  'Crear paginas de aterrizaje y guardar versiones',             'tenant'),
('templates', 'pages', 'update',  'Editar nombre, direccion e indexacion de una pagina',         'tenant'),
('templates', 'pages', 'delete',  'Borrar paginas de aterrizaje archivadas',                     'tenant'),
('templates', 'pages', 'publish', 'Publicar y retirar paginas de aterrizaje',                    'tenant')
ON CONFLICT (module, resource, action) DO NOTHING;
