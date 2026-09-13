-- Schema: access_control | Service: reputation
--
-- Permisos del modulo reputation: la reputacion de envio de la empresa y, para la
-- plataforma, la de todas las empresas. tenants/* solo tiene efecto junto con el rol
-- superadmin: el servicio exige ese rol en las rutas de plataforma, asi que concederlo a
-- otro rol no le abre nada. Idempotente por UNIQUE (module, resource, action).

INSERT INTO access_control.permissions (module, resource, action, description) VALUES
('reputation', 'status',  'read',   'Ver la reputacion de envio, los limites y el uso de la empresa'),
('reputation', 'history', 'read',   'Ver el historial de estados de reputacion de la empresa'),
('reputation', 'tenants', 'read',   'Ver la reputacion de envio de todas las empresas (plataforma)'),
('reputation', 'tenants', 'update', 'Fijar limites, suspender y liberar el envio de una empresa (plataforma)')
ON CONFLICT (module, resource, action) DO NOTHING;
