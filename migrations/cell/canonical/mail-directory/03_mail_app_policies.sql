-- Schema: mail | Service: mail-directory
--
-- Lo que el servicio mail-directory necesita del rol mail_app y que la 02 no cubria:
--
--   1. Borrar el uso de cuota de un buzon al darlo de baja. La 02 solo daba SELECT sobre
--      quota_usage al servicio; sin politica de DELETE el borrado no falla, simplemente
--      no borra nada, y la fila queda huerfana. La politica exige que el buzon exista, asi
--      que el servicio borra la cuota ANTES que el buzon.
--   2. Rutas de transporte de plataforma (tenant_id NULL). La 02 las deja leer a todas
--      las empresas pero no escribirlas a nadie bajo mail_app. Solo escribe quien tiene
--      autoridad administrativa (app.is_privileged); que ademas sea el operador de la
--      plataforma y no el administrador de una empresa lo exige el servicio, que es quien
--      conoce el rol. La lectura no cambia.
--   3. Saber si un nombre ya es dominio o dominio alias de OTRA empresa sin ver sus filas:
--      funcion SECURITY DEFINER que responde si/no. Sin ella, una empresa podria registrar
--      como dominio alias el dominio de otra y recibir su correo.
--
-- Idempotente y aditiva.

DROP POLICY IF EXISTS app_delete ON mail.quota_usage;
CREATE POLICY app_delete ON mail.quota_usage FOR DELETE TO mail_app
    USING (EXISTS (SELECT 1 FROM mail.mailboxes m WHERE m.username = quota_usage.username AND m.tenant_id = mail.current_tenant()));

DROP POLICY IF EXISTS tenant_isolation ON mail.transports;
CREATE POLICY tenant_isolation ON mail.transports FOR ALL TO mail_app
    USING (tenant_id IS NULL OR tenant_id = mail.current_tenant())
    WITH CHECK (
        tenant_id = mail.current_tenant()
        OR (tenant_id IS NULL AND current_setting('app.is_privileged', true) = 'true')
    );

CREATE OR REPLACE FUNCTION mail.name_in_use(p_name text) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = mail, pg_temp AS $$
    SELECT EXISTS (SELECT 1 FROM mail.domains WHERE domain = p_name)
        OR EXISTS (SELECT 1 FROM mail.alias_domains WHERE alias_domain = p_name)
$$;
REVOKE ALL ON FUNCTION mail.name_in_use(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION mail.name_in_use(text) TO mail_app;
