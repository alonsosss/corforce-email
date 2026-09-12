-- Schema: access_control | Service: access-control
--
-- Catalogo de permisos de los modulos del plano de control. Cada servicio de negocio
-- (dominios, casillas de correo, campanas...) siembra los suyos en su propia migracion
-- cuando existe. Idempotente por UNIQUE (module, resource, action).
--
-- Aqui no se siembran roles ni role_permissions: los roles pertenecen a cada tenant y
-- los crea organization al darlo de alta, con los permisos que le correspondan.

INSERT INTO access_control.permissions (module, resource, action, description) VALUES
-- organization
('organization', 'tenants',    'read',   'Ver empresas'),
('organization', 'tenants',    'create', 'Crear empresas'),
('organization', 'tenants',    'update', 'Modificar empresas'),
('organization', 'tenants',    'delete', 'Dar de baja empresas'),
('organization', 'modules',    'read',   'Ver modulos contratados'),
('organization', 'modules',    'update', 'Habilitar o deshabilitar modulos'),
('organization', 'migrations', 'read',   'Ver estado de migraciones'),
('organization', 'migrations', 'run',    'Ejecutar migraciones'),

-- identity
('identity', 'users',             'read',   'Ver usuarios'),
('identity', 'users',             'create', 'Crear usuarios'),
('identity', 'users',             'update', 'Modificar usuarios'),
('identity', 'users',             'delete', 'Desactivar usuarios'),
('identity', 'sessions',          'read',   'Ver sesiones'),
('identity', 'sessions',          'revoke', 'Revocar sesiones'),
('identity', 'session_policies',  'read',   'Ver politica de sesion'),
('identity', 'session_policies',  'update', 'Modificar politica de sesion'),
('identity', 'password_policies', 'read',   'Ver politica de contrasenas'),
('identity', 'password_policies', 'update', 'Modificar politica de contrasenas'),

-- access
('access', 'roles',       'read',   'Ver roles'),
('access', 'roles',       'create', 'Crear roles'),
('access', 'roles',       'update', 'Modificar roles y sus permisos'),
('access', 'roles',       'delete', 'Eliminar roles'),
('access', 'permissions', 'read',   'Ver catalogo de permisos'),
('access', 'user_roles',  'read',   'Ver roles de un usuario'),
('access', 'user_roles',  'assign', 'Asignar roles a usuarios'),
('access', 'user_roles',  'revoke', 'Revocar roles de usuarios'),
('access', 'denials',     'read',   'Ver accesos denegados'),

-- audit
('audit', 'logs',            'read',        'Ver rastro de auditoria'),
('audit', 'logs',            'create',      'Registrar eventos de auditoria'),
('audit', 'security_events', 'read',        'Ver eventos de seguridad'),
('audit', 'security_events', 'acknowledge', 'Atender eventos de seguridad'),
('audit', 'integrity',       'read',        'Ver estado de integridad del rastro'),
('audit', 'integrity',       'verify',      'Verificar la cadena de integridad'),

-- scheduler
('scheduler', 'jobs',       'read',   'Ver trabajos programados'),
('scheduler', 'jobs',       'create', 'Crear trabajos programados'),
('scheduler', 'jobs',       'update', 'Modificar, habilitar o deshabilitar trabajos'),
('scheduler', 'jobs',       'delete', 'Eliminar trabajos programados'),
('scheduler', 'jobs',       'run',    'Lanzar un trabajo manualmente'),
('scheduler', 'executions', 'read',   'Ver ejecuciones'),
('scheduler', 'executions', 'cancel', 'Cancelar ejecuciones'),
('scheduler', 'executions', 'retry',  'Reintentar ejecuciones'),
('scheduler', 'tasks',      'read',   'Ver tareas pendientes'),
('scheduler', 'tasks',      'create', 'Programar tareas'),
('scheduler', 'tasks',      'cancel', 'Cancelar tareas')
ON CONFLICT (module, resource, action) DO NOTHING;
