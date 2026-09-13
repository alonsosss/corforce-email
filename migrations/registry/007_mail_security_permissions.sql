-- Schema: access_control | Service: mail-security
--
-- Permisos del modulo mail_security: una triple (module, resource, action) por cada
-- accion que exponen los handlers de /api/v1/mail-security. El gateway gatea por modulo
-- y el handler exige la accion concreta (pkg/authz.RequirePermission). Idempotente.

INSERT INTO access_control.permissions (module, resource, action, description) VALUES
('mail_security', 'spam_scores',         'read',    'Ver umbrales de spam por buzon o dominio'),
('mail_security', 'spam_scores',         'update',  'Fijar umbrales de spam por buzon o dominio'),
('mail_security', 'spam_scores',         'delete',  'Quitar umbrales de spam'),
('mail_security', 'address_lists',       'read',    'Ver listas blancas y negras'),
('mail_security', 'address_lists',       'create',  'Anadir entradas a listas blancas y negras'),
('mail_security', 'address_lists',       'delete',  'Quitar entradas de listas blancas y negras'),
('mail_security', 'settings_maps',       'read',    'Ver bloques de configuracion adicional de Rspamd'),
('mail_security', 'settings_maps',       'create',  'Crear bloques de configuracion adicional de Rspamd'),
('mail_security', 'settings_maps',       'update',  'Modificar bloques de configuracion adicional de Rspamd'),
('mail_security', 'settings_maps',       'delete',  'Eliminar bloques de configuracion adicional de Rspamd'),
('mail_security', 'footers',             'read',    'Ver pies de pagina por dominio'),
('mail_security', 'footers',             'update',  'Fijar el pie de pagina de un dominio'),
('mail_security', 'footers',             'delete',  'Quitar el pie de pagina de un dominio'),
('mail_security', 'forwarding_hosts',    'read',    'Ver hosts de reenvio de confianza'),
('mail_security', 'forwarding_hosts',    'create',  'Anadir hosts de reenvio de confianza'),
('mail_security', 'forwarding_hosts',    'delete',  'Quitar hosts de reenvio de confianza'),
('mail_security', 'rate_limits',         'read',    'Ver limites de envio'),
('mail_security', 'rate_limits',         'update',  'Fijar limites de envio'),
('mail_security', 'rate_limits',         'delete',  'Quitar limites de envio'),
('mail_security', 'mailbox_tags',        'read',    'Ver el tratamiento de etiquetas +tag por buzon'),
('mail_security', 'mailbox_tags',        'update',  'Fijar el tratamiento de etiquetas +tag por buzon'),
('mail_security', 'quarantine',          'read',    'Ver la cuarentena'),
('mail_security', 'quarantine',          'delete',  'Eliminar elementos de la cuarentena'),
('mail_security', 'quarantine',          'release', 'Liberar elementos de la cuarentena'),
('mail_security', 'quarantine',          'learn',   'Entrenar Rspamd con elementos de la cuarentena'),
('mail_security', 'quarantine_settings', 'read',    'Ver ajustes de cuarentena'),
('mail_security', 'quarantine_settings', 'update',  'Modificar ajustes de cuarentena')
ON CONFLICT (module, resource, action) DO NOTHING;
