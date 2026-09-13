-- Schema: access_control | Service: billing
--
-- Permisos del modulo billing. subscription y usage son de la empresa: ver su plan y su
-- consumo. plans y subscriptions operan la plataforma: 018 los marca con alcance
-- 'platform' (nunca van a un rol de empresa) y el servicio exige ademas el rol
-- superadmin. Idempotente por UNIQUE (module, resource, action).

INSERT INTO access_control.permissions (module, resource, action, description) VALUES
('billing', 'subscription',  'read',   'Ver el plan, los limites y el periodo de la empresa'),
('billing', 'usage',         'read',   'Ver el consumo de la empresa frente a su plan'),
('billing', 'plans',         'read',   'Ver el catalogo de planes de la plataforma'),
('billing', 'plans',         'create', 'Crear planes de la plataforma'),
('billing', 'plans',         'update', 'Editar planes que aun no tienen suscripciones'),
('billing', 'plans',         'delete', 'Retirar planes del catalogo'),
('billing', 'subscriptions', 'read',   'Ver las suscripciones de todas las empresas'),
('billing', 'subscriptions', 'update', 'Asignar o cambiar el plan y el estado de una empresa')
ON CONFLICT (module, resource, action) DO NOTHING;
