-- Schema: mail | Service: mail-directory
--
-- Aislamiento entre empresas dentro del directorio de la celda. Las tablas de mail
-- comparten base entre todas las empresas de la celda; esta migracion hace que los
-- servicios Go solo vean las filas de la empresa de la peticion.
--
-- Dos roles, dos politicas:
--   mail_app    -> los servicios Go. Entran con SET LOCAL ROLE mail_app dentro de la
--                  transaccion (pkg/db.TransactRLS) y solo ven tenant_id =
--                  app.current_tenant_id. Sin ese GUC no ven nada: fail-closed.
--   mail_engine -> Postfix y Dovecot. No saben de empresas: resuelven un destinatario
--                  entre todos los dominios de la celda. Su politica es USING (true) y
--                  sigue acotada por los GRANT de la 01 (solo lectura, sin credenciales).
--
-- Postgres exime al dueno de las tablas de sus politicas y la aplicacion conecta como
-- dueno, por eso el cambio de rol dentro de la transaccion es lo que las hace efectivas.
-- Sin FORCE ROW LEVEL SECURITY a proposito: el dueno sigue exento y pg_dump respalda
-- todo. Idempotente.

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'mail_app') THEN
        CREATE ROLE mail_app NOLOGIN;
    END IF;
END $$;

-- El usuario de la aplicacion tiene que poder ponerse el sombrero de mail_app.
DO $$
BEGIN
    EXECUTE format('GRANT mail_app TO %I', current_user);
END $$;

GRANT USAGE ON SCHEMA mail TO mail_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA mail TO mail_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA mail GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO mail_app;

CREATE OR REPLACE FUNCTION mail.current_tenant() RETURNS uuid
LANGUAGE sql STABLE AS $$
    SELECT NULLIF(current_setting('app.current_tenant_id', true), '')::uuid
$$;

DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['domains','alias_domains','mailboxes','aliases','spam_aliases',
        'sender_acl','app_passwords','relayhosts','transports','tls_policy_overrides',
        'recipient_maps','bcc_maps','sieve_filters','sasl_logins']
    LOOP
        EXECUTE format('ALTER TABLE mail.%I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON mail.%I', t);
        EXECUTE format(
            'CREATE POLICY tenant_isolation ON mail.%I FOR ALL TO mail_app '
            'USING (tenant_id = mail.current_tenant()) WITH CHECK (tenant_id = mail.current_tenant())', t);
        EXECUTE format('DROP POLICY IF EXISTS engine_read ON mail.%I', t);
        EXECUTE format('CREATE POLICY engine_read ON mail.%I FOR SELECT TO mail_engine USING (true)', t);
    END LOOP;
END $$;

-- transports.tenant_id admite NULL para las rutas de plataforma; la aplicacion las ve
-- ademas de las suyas.
DROP POLICY IF EXISTS tenant_isolation ON mail.transports;
CREATE POLICY tenant_isolation ON mail.transports FOR ALL TO mail_app
    USING (tenant_id IS NULL OR tenant_id = mail.current_tenant())
    WITH CHECK (tenant_id = mail.current_tenant());

-- quota_usage no tiene tenant_id (la escribe Dovecot): la aplicacion la lee unida a sus
-- buzones y el motor la escribe entera.
ALTER TABLE mail.quota_usage ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS app_read ON mail.quota_usage;
CREATE POLICY app_read ON mail.quota_usage FOR SELECT TO mail_app
    USING (EXISTS (SELECT 1 FROM mail.mailboxes m WHERE m.username = quota_usage.username AND m.tenant_id = mail.current_tenant()));
DROP POLICY IF EXISTS engine_all ON mail.quota_usage;
CREATE POLICY engine_all ON mail.quota_usage FOR ALL TO mail_engine USING (true) WITH CHECK (true);

-- Vistas publicadas para los servicios vecinos de la celda (mail-security resuelve
-- destinatarios finales): columnas enumeradas, sin credenciales ni cuotas.
CREATE OR REPLACE VIEW mail.v_routing_aliases AS
    SELECT tenant_id, address, goto, domain, active FROM mail.aliases;
CREATE OR REPLACE VIEW mail.v_routing_alias_domains AS
    SELECT tenant_id, alias_domain, target_domain, active FROM mail.alias_domains;
CREATE OR REPLACE VIEW mail.v_routing_mailboxes AS
    SELECT tenant_id, username, local_part, domain, active, kind FROM mail.mailboxes;
CREATE OR REPLACE VIEW mail.v_routing_domains AS
    SELECT tenant_id, domain, active, backupmx FROM mail.domains;
GRANT SELECT ON mail.v_routing_aliases, mail.v_routing_alias_domains, mail.v_routing_mailboxes, mail.v_routing_domains TO mail_app;
