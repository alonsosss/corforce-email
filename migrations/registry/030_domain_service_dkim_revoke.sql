-- Schema: access_control | Service: domain-service
--
-- Revocar la clave DKIM comprometida de un dominio: retira de inmediato de los motores de la
-- celda todas las claves que tenia y firma con una nueva cuyo TXT aun no esta publicado, asi que
-- el correo del dominio puede no superar DKIM hasta que el cliente lo publique. Es una accion
-- de respuesta a un incidente, aparte de rotate_dkim (rotacion programada con gracia), para que
-- un rol que rota por calendario no pueda cortar la firma. Alcance de empresa: llega al
-- tenant_admin al sembrar el rol y con el resembrado de roles. Idempotente.

INSERT INTO access_control.permissions (module, resource, action, description, scope) VALUES
('domains', 'domains', 'revoke_dkim', 'Revocar de inmediato la clave DKIM comprometida de un dominio', 'tenant')
ON CONFLICT (module, resource, action) DO NOTHING;
