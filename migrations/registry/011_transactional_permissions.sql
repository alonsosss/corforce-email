-- Schema: access_control | Service: transactional
--
-- Permisos del modulo transactional: consultar y crear envios, ver las estadisticas y
-- la proyeccion de dominios de envio. Idempotente por UNIQUE (module, resource, action).

INSERT INTO access_control.permissions (module, resource, action, description) VALUES
('transactional', 'messages',        'read',   'Ver los envios transaccionales'),
('transactional', 'messages',        'create', 'Enviar correo transaccional'),
('transactional', 'stats',           'read',   'Ver estadisticas de envio transaccional'),
('transactional', 'sending_domains', 'read',   'Ver los dominios de envio verificados')
ON CONFLICT (module, resource, action) DO NOTHING;
