-- Schema: access_control | Service: mail-security
--
-- Permisos nuevos del modulo mail_security (handlers de /api/v1/mail-security):
--   smtp_access (empresa): redes desde las que un buzon puede enviar por SMTP
--     autenticado (SMTP_LIMITED_ACCESS y SMTP_ALLOW_NETS_<usuario> de Rspamd).
--   firewall (plataforma): listas y opciones del cortafuegos de la celda (netfilter) y
--     desbaneo. Solo el superadmin: nunca va a un rol de empresa (018_permission_scope), y
--     el servicio vuelve a exigir el operador porque el cortafuegos es de toda la celda.
-- Idempotente.

INSERT INTO access_control.permissions (module, resource, action, description, scope) VALUES
('mail_security', 'smtp_access', 'read',   'Ver las redes desde las que un buzon puede enviar por SMTP', 'tenant'),
('mail_security', 'smtp_access', 'update', 'Fijar las redes desde las que un buzon puede enviar por SMTP', 'tenant'),
('mail_security', 'smtp_access', 'delete', 'Quitar la restriccion de redes SMTP de un buzon', 'tenant'),
('mail_security', 'firewall',    'read',   'Ver listas, opciones y baneos del cortafuegos de la celda', 'platform'),
('mail_security', 'firewall',    'create', 'Anadir redes a las listas del cortafuegos de la celda', 'platform'),
('mail_security', 'firewall',    'update', 'Modificar las opciones del cortafuegos y desbanear redes', 'platform'),
('mail_security', 'firewall',    'delete', 'Quitar redes de las listas del cortafuegos de la celda', 'platform')
ON CONFLICT (module, resource, action) DO NOTHING;

-- Si alguno ya existia con otro alcance, el del cortafuegos queda de plataforma y fuera de
-- cualquier rol de empresa.
UPDATE access_control.permissions SET scope = 'platform'
 WHERE module = 'mail_security' AND resource = 'firewall' AND scope <> 'platform';
DELETE FROM access_control.role_permissions rp
 USING access_control.permissions p
 WHERE rp.permission_id = p.id AND p.module = 'mail_security' AND p.resource = 'firewall';
