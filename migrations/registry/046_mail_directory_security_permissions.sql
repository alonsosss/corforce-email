-- Schema: access_control | Service: mail-directory
--
-- Permisos de la seguridad de los buzones (docs/Plan_Webmail_Seguridad.md; rutas de mail-directory en
-- el modulo mailboxes):
--   mail_policy (empresa): ver (read) y cambiar (update) la politica de correo de la empresa, hoy si
--     sus buzones pueden reenviar a direcciones externas. Apagarlo retira los reenvios externos ya
--     guardados de todos los buzones, por eso es un permiso propio y no el de editar buzones.
--   mailbox_mfa (empresa): restablecer (delete) la verificacion en dos pasos de un buzon cuyo titular
--     perdio su aplicacion y sus codigos de recuperacion. Cierra sus sesiones del webmail.
-- Alcance de empresa: llegan al tenant_admin al sembrar el rol y con el resembrado de roles.
-- Idempotente.

INSERT INTO access_control.permissions (module, resource, action, description, scope) VALUES
('mailboxes', 'mail_policy', 'read',   'Ver la politica de correo de la empresa (reenvio a direcciones externas)', 'tenant'),
('mailboxes', 'mail_policy', 'update', 'Cambiar la politica de correo de la empresa (reenvio a direcciones externas)', 'tenant'),
('mailboxes', 'mailbox_mfa', 'delete', 'Restablecer la verificacion en dos pasos de un buzon', 'tenant')
ON CONFLICT (module, resource, action) DO NOTHING;
