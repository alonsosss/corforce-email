-- Schema: access_control | Service: automations
--
-- Permisos del modulo automations (alcance tenant, el de la columna por defecto).
-- workflows/activate cubre activar, pausar y archivar: los tres gobiernan si un flujo
-- corre. settings cubre el correo del doble opt-in y su historial. Idempotente por
-- UNIQUE (module, resource, action).

INSERT INTO access_control.permissions (module, resource, action, description) VALUES
('automations', 'workflows', 'read',     'Ver los flujos de automatizacion'),
('automations', 'workflows', 'create',   'Crear flujos de automatizacion en borrador'),
('automations', 'workflows', 'update',   'Editar flujos en borrador o en pausa'),
('automations', 'workflows', 'delete',   'Eliminar flujos en borrador o archivados'),
('automations', 'workflows', 'activate', 'Activar, pausar y archivar flujos'),
('automations', 'runs',      'read',     'Ver las ejecuciones de los flujos por contacto'),
('automations', 'settings',  'read',     'Ver la configuracion y el historial del doble opt-in'),
('automations', 'settings',  'update',   'Configurar el correo del doble opt-in')
ON CONFLICT (module, resource, action) DO NOTHING;
