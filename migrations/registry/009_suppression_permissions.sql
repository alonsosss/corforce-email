-- Schema: access_control | Service: suppression
--
-- Permisos del modulo suppression: la lista de exclusiones de envio de la empresa.
-- delete es el unico permiso que reactiva una direccion suprimida por rebote duro o
-- queja, y esa operacion queda auditada por evento. Idempotente por UNIQUE (module,
-- resource, action).

INSERT INTO access_control.permissions (module, resource, action, description) VALUES
('suppression', 'entries', 'read',   'Ver la lista de exclusiones de envio'),
('suppression', 'entries', 'create', 'Excluir direcciones de envio manualmente'),
('suppression', 'entries', 'delete', 'Reactivar direcciones excluidas por rebote o queja'),
('suppression', 'entries', 'import', 'Cargar exclusiones de envio en bloque'),
('suppression', 'stats',   'read',   'Ver estadisticas de exclusiones de envio')
ON CONFLICT (module, resource, action) DO NOTHING;
