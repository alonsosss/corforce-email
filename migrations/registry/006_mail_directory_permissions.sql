-- Schema: access_control | Service: mail-directory
--
-- Permisos del directorio de correo: exactamente las triples que exigen las rutas de
-- mail-directory (modulos domains, mailboxes y mail_routing). Los permisos de
-- verificacion de dominios (domains/*) los siembra domain-service. Idempotente por
-- UNIQUE (module, resource, action).

INSERT INTO access_control.permissions (module, resource, action, description) VALUES
-- domains (directorio: parte de enrutado)
('domains', 'domains',       'read',   'Ver dominios de correo'),
('domains', 'domains',       'create', 'Dar de alta dominios de correo'),
('domains', 'domains',       'update', 'Modificar dominios de correo'),
('domains', 'domains',       'delete', 'Eliminar dominios de correo'),
('domains', 'alias_domains', 'read',   'Ver dominios alias'),
('domains', 'alias_domains', 'create', 'Crear dominios alias'),
('domains', 'alias_domains', 'update', 'Modificar dominios alias'),
('domains', 'alias_domains', 'delete', 'Eliminar dominios alias'),

-- mailboxes
('mailboxes', 'mailboxes',     'read',         'Ver buzones'),
('mailboxes', 'mailboxes',     'create',       'Crear buzones'),
('mailboxes', 'mailboxes',     'update',       'Modificar buzones'),
('mailboxes', 'mailboxes',     'delete',       'Eliminar buzones'),
('mailboxes', 'mailboxes',     'set_password', 'Cambiar la contrasena de un buzon'),
('mailboxes', 'app_passwords', 'read',         'Ver contrasenas de aplicacion'),
('mailboxes', 'app_passwords', 'create',       'Crear contrasenas de aplicacion'),
('mailboxes', 'app_passwords', 'update',       'Modificar contrasenas de aplicacion'),
('mailboxes', 'app_passwords', 'delete',       'Revocar contrasenas de aplicacion'),
('mailboxes', 'sieve',         'read',         'Ver filtros sieve de un buzon'),
('mailboxes', 'sieve',         'update',       'Modificar filtros sieve de un buzon'),

-- mail_routing
('mail_routing', 'aliases',        'read',   'Ver aliases'),
('mail_routing', 'aliases',        'create', 'Crear aliases'),
('mail_routing', 'aliases',        'update', 'Modificar aliases'),
('mail_routing', 'aliases',        'delete', 'Eliminar aliases'),
('mail_routing', 'spam_aliases',   'read',   'Ver aliases temporales'),
('mail_routing', 'spam_aliases',   'create', 'Crear aliases temporales'),
('mail_routing', 'spam_aliases',   'update', 'Modificar aliases temporales'),
('mail_routing', 'spam_aliases',   'delete', 'Eliminar aliases temporales'),
('mail_routing', 'sender_acl',     'read',   'Ver permisos de remitente'),
('mail_routing', 'sender_acl',     'create', 'Crear permisos de remitente'),
('mail_routing', 'sender_acl',     'update', 'Modificar permisos de remitente'),
('mail_routing', 'sender_acl',     'delete', 'Eliminar permisos de remitente'),
('mail_routing', 'relayhosts',     'read',   'Ver relayhosts'),
('mail_routing', 'relayhosts',     'create', 'Crear relayhosts'),
('mail_routing', 'relayhosts',     'update', 'Modificar relayhosts'),
('mail_routing', 'relayhosts',     'delete', 'Eliminar relayhosts'),
('mail_routing', 'transports',     'read',   'Ver transportes'),
('mail_routing', 'transports',     'create', 'Crear transportes'),
('mail_routing', 'transports',     'update', 'Modificar transportes'),
('mail_routing', 'transports',     'delete', 'Eliminar transportes'),
('mail_routing', 'tls_policies',   'read',   'Ver politicas TLS'),
('mail_routing', 'tls_policies',   'create', 'Crear politicas TLS'),
('mail_routing', 'tls_policies',   'update', 'Modificar politicas TLS'),
('mail_routing', 'tls_policies',   'delete', 'Eliminar politicas TLS'),
('mail_routing', 'recipient_maps', 'read',   'Ver mapas de destinatario'),
('mail_routing', 'recipient_maps', 'create', 'Crear mapas de destinatario'),
('mail_routing', 'recipient_maps', 'update', 'Modificar mapas de destinatario'),
('mail_routing', 'recipient_maps', 'delete', 'Eliminar mapas de destinatario'),
('mail_routing', 'bcc_maps',       'read',   'Ver mapas BCC'),
('mail_routing', 'bcc_maps',       'create', 'Crear mapas BCC'),
('mail_routing', 'bcc_maps',       'update', 'Modificar mapas BCC'),
('mail_routing', 'bcc_maps',       'delete', 'Eliminar mapas BCC')
ON CONFLICT (module, resource, action) DO NOTHING;
