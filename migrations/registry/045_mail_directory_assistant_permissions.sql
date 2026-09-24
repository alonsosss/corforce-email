-- Schema: access_control | Service: mail-directory
--
-- Permisos del ajuste del asistente del webmail (rutas /api/v1/mail-directory/assistant de
-- mail-directory, en el modulo mailboxes; docs/adr/0015-asistente-del-webmail-con-claude.md):
--   assistant_settings (empresa): ver si el asistente esta activado para la empresa (read) y activarlo o
--     apagarlo (update). Activarlo hace que el texto que cada usuario pida procesar salga a un proveedor
--     externo, por eso es un permiso propio y no el de editar buzones. Alcance de empresa: llegan al
--     tenant_admin al sembrar el rol y con el resembrado de roles. Idempotente.

INSERT INTO access_control.permissions (module, resource, action, description, scope) VALUES
('mailboxes', 'assistant_settings', 'read',   'Ver si el asistente del webmail esta activado para la empresa', 'tenant'),
('mailboxes', 'assistant_settings', 'update', 'Activar o apagar el asistente del webmail para la empresa', 'tenant')
ON CONFLICT (module, resource, action) DO NOTHING;
