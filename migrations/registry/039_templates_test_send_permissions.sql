-- Schema: access_control | Service: templates
--
-- Permiso del envio de prueba de una version de plantilla (docs/Plan_Marketing_Avanzado.md,
-- 1-A): POST /api/v1/templates/{id}/versions/{v}/test-send. Es una accion propia y no la de
-- renderizar porque saca correo real hacia fuera, aunque no se facture. Alcance de empresa:
-- llega al tenant_admin al sembrar el rol y con el resembrado de roles. Idempotente.
--   test_send/create  enviar una version, tambien un borrador, a hasta cinco direcciones de prueba

INSERT INTO access_control.permissions (module, resource, action, description, scope) VALUES
('templates', 'test_send', 'create', 'Enviar pruebas de una version de plantilla', 'tenant')
ON CONFLICT (module, resource, action) DO NOTHING;
