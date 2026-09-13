-- Schema: access_control | Service: access-control
--
-- Permisos que exigen las rutas del plano de control y que 005 no sembraba. Los de
-- plataforma (directorio de celdas, sesiones de todas las empresas) solo tienen efecto
-- junto con el rol superadmin, que los servicios exigen ademas en esas rutas; nunca se
-- asignan a un rol de empresa. Fijar la contrasena de otro usuario es de empresa y llega
-- al tenant_admin con el resembrado de roles. Idempotente por UNIQUE (module, resource, action).

INSERT INTO access_control.permissions (module, resource, action, description, scope) VALUES
('organization', 'cells',             'read',           'Ver el directorio de celdas (plataforma)',                  'platform'),
('organization', 'cells',             'create',         'Dar de alta celdas (plataforma)',                           'platform'),
('organization', 'cells',             'update',         'Cambiar el estado o la region de una celda (plataforma)',   'platform'),
('identity',     'platform_sessions', 'read',           'Ver las sesiones de todas las empresas (plataforma)',       'platform'),
('identity',     'users',             'reset_password', 'Fijar una contrasena nueva a otro usuario de la empresa',   'tenant')
ON CONFLICT (module, resource, action) DO NOTHING;
