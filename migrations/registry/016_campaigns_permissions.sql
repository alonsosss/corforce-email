-- Schema: access_control | Service: campaigns
--
-- Permisos del modulo campaigns. send cubre programar, iniciar, reanudar y el envio de
-- prueba; cancel, pausar y cancelar. Idempotente por UNIQUE (module, resource, action).

INSERT INTO access_control.permissions (module, resource, action, description) VALUES
('campaigns', 'campaigns', 'read',   'Ver las campanas de marketing'),
('campaigns', 'campaigns', 'create', 'Crear campanas de marketing en borrador'),
('campaigns', 'campaigns', 'update', 'Editar campanas en borrador o en pausa'),
('campaigns', 'campaigns', 'delete', 'Eliminar campanas en borrador o canceladas'),
('campaigns', 'campaigns', 'send',   'Programar, iniciar, reanudar y probar campanas'),
('campaigns', 'campaigns', 'cancel', 'Pausar y cancelar campanas'),
('campaigns', 'stats',     'read',   'Ver estadisticas y lotes de envio de las campanas')
ON CONFLICT (module, resource, action) DO NOTHING;
