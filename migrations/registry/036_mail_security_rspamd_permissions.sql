-- Schema: access_control | Service: mail-security
--
-- Permiso de lectura del controller de Rspamd de la celda (handlers de /api/v1/mail-security/rspamd,
-- docs/adr/0009):
--   rspamd/read (plataforma): ver los contadores del antispam (mensajes analizados, veredictos, clasificador
--     bayesiano, fuzzy, tiempos) y su historial reciente (sobre y veredicto de cada mensaje, nunca su
--     contenido). Rspamd analiza el correo de todas las empresas de la celda: solo el superadmin, nunca un
--     rol de empresa (018_permission_scope), y el servicio vuelve a exigir el operador. No existe ninguna
--     accion de escritura sobre el controller. Idempotente.

INSERT INTO access_control.permissions (module, resource, action, description, scope) VALUES
('mail_security', 'rspamd', 'read', 'Ver los contadores y el historial reciente del antispam de la celda', 'platform')
ON CONFLICT (module, resource, action) DO NOTHING;

UPDATE access_control.permissions SET scope = 'platform'
 WHERE module = 'mail_security' AND resource = 'rspamd' AND scope <> 'platform';
DELETE FROM access_control.role_permissions rp
 USING access_control.permissions p
 WHERE rp.permission_id = p.id AND p.module = 'mail_security' AND p.resource = 'rspamd';
