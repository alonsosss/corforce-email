-- Schema: access_control | Service: analytics
--
-- Permisos del modulo analytics. El modulo se contrata con el catalogo 'marketing'
-- (organization.module_catalog.permission_modules); aqui solo se declara la accion que el
-- handler exige ademas del gateo por modulo del gateway: leer los informes de envio.
-- Idempotente por UNIQUE (module, resource, action).

INSERT INTO access_control.permissions (module, resource, action, description) VALUES
('analytics', 'reports', 'read', 'Ver informes de envio: totales, tasas, series diarias, campanas y dominios destino')
ON CONFLICT (module, resource, action) DO NOTHING;
