-- Schema: mail_files | Service: mail-files
--
-- Rol de servicio del esquema mail_files en la base de CADA empresa, con el patron de
-- templates/02_service_role.sql: el grupo NOLOGIN mail_files_service tiene los permisos, que se
-- enumeran aqui, y el rol de login mail_svc_mail_files (ops/db/tenant-service-role.sh) es miembro
-- suyo. Solo DML sobre su esquema: ni DDL, ni TRUNCATE, ni dueno.
--
-- Aislamiento por empresa con RLS: toda fila tiene que ser de la empresa de la sesion
-- (app.current_tenant_id, que fija pkg/db al abrir la transaccion). La politica es por empresa y no
-- por buzon porque la descarga publica y el barrido no actuan como un buzon; el buzon dueno lo exige
-- cada consulta del remitente (mailbox_id en el WHERE).
--
-- Idempotente y aditiva.

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'mail_files_service') THEN
        BEGIN
            CREATE ROLE mail_files_service NOLOGIN;
        EXCEPTION WHEN duplicate_object OR unique_violation THEN
            -- Los roles son del cluster: otra base de empresa que migraba a la vez lo acaba de crear.
            NULL;
        END;
    END IF;
END $$;

CREATE OR REPLACE FUNCTION mail_files.current_tenant() RETURNS uuid
LANGUAGE sql STABLE AS $$
    SELECT NULLIF(current_setting('app.current_tenant_id', true), '')::uuid
$$;

GRANT USAGE ON SCHEMA mail_files TO mail_files_service;
GRANT EXECUTE ON FUNCTION mail_files.current_tenant() TO mail_files_service;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA mail_files TO mail_files_service;
ALTER DEFAULT PRIVILEGES IN SCHEMA mail_files
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO mail_files_service;

ALTER TABLE mail_files.shared_files ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON mail_files.shared_files;
CREATE POLICY tenant_isolation ON mail_files.shared_files FOR ALL TO mail_files_service
    USING (tenant_id = mail_files.current_tenant())
    WITH CHECK (tenant_id = mail_files.current_tenant());

DO $$
BEGIN
    EXECUTE format('GRANT CONNECT ON DATABASE %I TO mail_files_service', current_database());
END $$;
