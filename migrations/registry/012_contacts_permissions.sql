-- Schema: access_control | Service: contacts
--
-- Permisos de los modulos contacts (contactos, listas, atributos, evidencia de
-- consentimiento) y segments (segmentos dinamicos), ambos servidos por el servicio
-- contacts. delete de contacts es el derecho de supresion del titular y export su
-- derecho de acceso; preview de segments se gatea como lectura en el gateway
-- (read_posts). Idempotente por UNIQUE (module, resource, action).

INSERT INTO access_control.permissions (module, resource, action, description) VALUES
('contacts', 'contacts',   'read',    'Ver contactos'),
('contacts', 'contacts',   'create',  'Crear contactos'),
('contacts', 'contacts',   'update',  'Editar contactos'),
('contacts', 'contacts',   'delete',  'Borrar contactos (derecho de supresion del titular)'),
('contacts', 'contacts',   'import',  'Importar contactos y ver el historial de importaciones'),
('contacts', 'contacts',   'export',  'Exportar todos los datos de un contacto (derecho de acceso)'),
('contacts', 'lists',      'read',    'Ver listas de contactos'),
('contacts', 'lists',      'create',  'Crear listas de contactos'),
('contacts', 'lists',      'update',  'Editar listas y sus miembros'),
('contacts', 'lists',      'delete',  'Borrar listas de contactos'),
('contacts', 'attributes', 'read',    'Ver los atributos declarados de los contactos'),
('contacts', 'attributes', 'create',  'Declarar atributos de contacto'),
('contacts', 'attributes', 'update',  'Editar atributos de contacto'),
('contacts', 'attributes', 'delete',  'Retirar atributos de contacto'),
('contacts', 'consents',   'read',    'Ver la evidencia de consentimiento de un contacto'),
('contacts', 'consents',   'create',  'Registrar consentimiento y pedir el doble opt-in'),
('segments', 'segments',   'read',    'Ver segmentos y sus contactos'),
('segments', 'segments',   'create',  'Crear segmentos'),
('segments', 'segments',   'update',  'Editar segmentos'),
('segments', 'segments',   'delete',  'Borrar segmentos'),
('segments', 'segments',   'preview', 'Previsualizar un segmento sin guardarlo')
ON CONFLICT (module, resource, action) DO NOTHING;
