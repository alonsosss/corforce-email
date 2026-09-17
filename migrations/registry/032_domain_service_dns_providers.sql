-- Schema: access_control | Service: domain-service
--
-- Publicacion automatica del DNS de los dominios en el proveedor DNS de la empresa (hoy
-- Cloudflare). Conectar guarda cifrado un token de terceros con permiso de escritura sobre las
-- zonas del cliente, asi que va aparte de leer el estado y de desconectar (que borra el token).
-- publish_dns cubre elegir el modo de publicacion de un dominio y publicar sus registros, y va
-- aparte de verify: escribe en la zona del cliente. Alcance de empresa: llega al tenant_admin al
-- sembrar el rol y con el resembrado de roles. Idempotente.

INSERT INTO access_control.permissions (module, resource, action, description, scope) VALUES
('domains', 'dns_providers', 'read', 'Ver la conexion de la empresa con su proveedor DNS', 'tenant'),
('domains', 'dns_providers', 'connect', 'Conectar el proveedor DNS de la empresa con un token de API', 'tenant'),
('domains', 'dns_providers', 'disconnect', 'Desconectar el proveedor DNS de la empresa y borrar su token', 'tenant'),
('domains', 'domains', 'publish_dns', 'Elegir como se publica el DNS de un dominio y publicar sus registros en el proveedor conectado', 'tenant')
ON CONFLICT (module, resource, action) DO NOTHING;
