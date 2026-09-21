-- Schema: access_control | Service: mail-security
--
-- Permisos del gestor de la cola de Postfix de la celda (handlers de /api/v1/mail-security/queue):
--   queue (plataforma): ver los mensajes de la cola con su motivo de diferimiento, reintentarlos,
--     retenerlos, liberarlos y vaciar la cola diferida (update), y borrarlos (delete). La cola mezcla el
--     correo de todas las empresas de la celda: solo el superadmin, nunca un rol de empresa
--     (018_permission_scope), y el servicio vuelve a exigir el operador. Idempotente.

INSERT INTO access_control.permissions (module, resource, action, description, scope) VALUES
('mail_security', 'queue', 'read',   'Ver los mensajes de la cola de Postfix de la celda y por que siguen en ella', 'platform'),
('mail_security', 'queue', 'update', 'Reintentar, retener y liberar mensajes de la cola de Postfix de la celda y vaciar la cola diferida', 'platform'),
('mail_security', 'queue', 'delete', 'Borrar mensajes de la cola de Postfix de la celda', 'platform')
ON CONFLICT (module, resource, action) DO NOTHING;

UPDATE access_control.permissions SET scope = 'platform'
 WHERE module = 'mail_security' AND resource = 'queue' AND scope <> 'platform';
DELETE FROM access_control.role_permissions rp
 USING access_control.permissions p
 WHERE rp.permission_id = p.id AND p.module = 'mail_security' AND p.resource = 'queue';
