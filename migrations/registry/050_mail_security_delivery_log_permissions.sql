-- Schema: access_control | Service: mail-security
--
-- Registro de entregas del correo corporativo (docs/Plan_Registro_de_Envios.md): ver lo que los
-- buzones de la empresa enviaron y recibieron, con el estado y el motivo de cada rechazo. Solo
-- lectura. Alcance de empresa: llega al tenant_admin al sembrar el rol y con el resembrado de
-- roles. Idempotente.

INSERT INTO access_control.permissions (module, resource, action, description, scope) VALUES
('mail_security', 'delivery_log', 'read', 'Ver el registro de entregas del correo corporativo', 'tenant')
ON CONFLICT (module, resource, action) DO NOTHING;
