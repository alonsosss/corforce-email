-- Schema: mail_dav | Service: mail-dav
--
-- Rol de servicio del esquema mail_dav en la base de CADA empresa, y las politicas de fila que acotan
-- cada consulta al buzon de la sesion.
--
-- Dos roles, el mismo patron que el resto de servicios de empresa: el grupo NOLOGIN mail_dav_service
-- tiene los permisos y las politicas, y el rol de login mail_svc_mail_dav, que crea
-- ops/db/tenant-service-role.sh con la contrasena del almacen, es miembro suyo. Solo DML: ni DDL, ni
-- TRUNCATE, ni dueno. Las politicas valen para ese login porque no es dueno de las tablas: Postgres
-- exime al dueno (las migraciones), asi que sin credencial propia (desarrollo) solo rigen los filtros
-- por tenant_id y mailbox_id de cada consulta.
--
-- Las politicas comparan tenant_id y mailbox_id con app.current_tenant_id y app.current_user_id de la
-- sesion. Sin esos valores no dejan pasar nada: fail-closed. mail-dav pone en app.current_user_id el id
-- del BUZON que autentico mail-auth, no el de un usuario de la plataforma.
--
-- Idempotente y aditiva.

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'mail_dav_service') THEN
        CREATE ROLE mail_dav_service NOLOGIN;
    END IF;
END $$;

CREATE OR REPLACE FUNCTION mail_dav.current_tenant() RETURNS uuid
LANGUAGE sql STABLE AS $$
    SELECT NULLIF(current_setting('app.current_tenant_id', true), '')::uuid
$$;

CREATE OR REPLACE FUNCTION mail_dav.current_mailbox() RETURNS uuid
LANGUAGE sql STABLE AS $$
    SELECT NULLIF(current_setting('app.current_user_id', true), '')::uuid
$$;

GRANT USAGE ON SCHEMA mail_dav TO mail_dav_service;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA mail_dav TO mail_dav_service;
ALTER DEFAULT PRIVILEGES IN SCHEMA mail_dav
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO mail_dav_service;

DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['addressbooks', 'contacts', 'collection_changes']
    LOOP
        EXECUTE format('ALTER TABLE mail_dav.%I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('DROP POLICY IF EXISTS mailbox_isolation ON mail_dav.%I', t);
        EXECUTE format(
            'CREATE POLICY mailbox_isolation ON mail_dav.%I FOR ALL TO mail_dav_service '
            'USING (tenant_id = mail_dav.current_tenant() AND mailbox_id = mail_dav.current_mailbox()) '
            'WITH CHECK (tenant_id = mail_dav.current_tenant() AND mailbox_id = mail_dav.current_mailbox())', t);
    END LOOP;
END $$;

-- CONNECT sobre la base de la empresa en la que corre esta migracion: una empresa nueva queda lista
-- sin un paso aparte (organization crea su base, la cierra a PUBLIC y le aplica las canonicas).
DO $$
BEGIN
    EXECUTE format('GRANT CONNECT ON DATABASE %I TO mail_dav_service', current_database());
END $$;
