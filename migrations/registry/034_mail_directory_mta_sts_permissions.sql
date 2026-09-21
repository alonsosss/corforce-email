-- Schema: access_control | Service: mail-directory
--
-- Permisos de la politica MTA-STS de un dominio (rutas /api/v1/mail-domains/mta-sts de mail-directory,
-- en el modulo domains):
--   mta_sts (empresa): ver el modo de MTA-STS de los dominios de la empresa (read) y cambiarlo (update),
--     incluido el paso de testing a enforce, que puede dejar de entregar correo si los MX o el
--     certificado no casan. Idempotente.

INSERT INTO access_control.permissions (module, resource, action, description, scope) VALUES
('domains', 'mta_sts', 'read',   'Ver el modo de MTA-STS de los dominios de correo', 'tenant'),
('domains', 'mta_sts', 'update', 'Cambiar el modo de MTA-STS de un dominio de correo, incluido enforce', 'tenant')
ON CONFLICT (module, resource, action) DO NOTHING;
