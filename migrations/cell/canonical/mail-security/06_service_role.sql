-- Schema: mail_security | Service: mail-security
--
-- Rol mail_service en mail_security: los endpoints de los motores (/settings, /pipe,
-- mapas), la reconciliacion de Redis, el consumo del directorio, el cortafuegos y el rele
-- de la outbox corrian como dueno de las tablas, exentos de RLS. Con la credencial propia
-- de la celda (ops/db/cell-service-role.sh) el servicio ya no es dueno: aqui recibe DML
-- sobre su esquema y la politica service_all en cada tabla con RLS, incluidas las de la
-- celda o la plataforma (engine_documents, firewall_*) que mail_app no puede tocar.
-- Sin DDL, sin TRUNCATE y sin ser dueno; mail_app no gana nada.
--
-- Toda tabla nueva con RLS en mail_security lleva su politica service_all en la misma
-- migracion que la crea; esta se la da a las existentes. Idempotente.

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'mail_service') THEN
        CREATE ROLE mail_service NOLOGIN;
    END IF;
END $$;

GRANT USAGE ON SCHEMA mail_security TO mail_service;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA mail_security TO mail_service;
ALTER DEFAULT PRIVILEGES IN SCHEMA mail_security GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO mail_service;

DO $$
DECLARE t text;
BEGIN
    FOR t IN
        SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
         WHERE n.nspname = 'mail_security' AND c.relkind IN ('r', 'p') AND c.relrowsecurity
    LOOP
        EXECUTE format('DROP POLICY IF EXISTS service_all ON mail_security.%I', t);
        EXECUTE format(
            'CREATE POLICY service_all ON mail_security.%I FOR ALL TO mail_service USING (true) WITH CHECK (true)', t);
    END LOOP;
END $$;

-- El rele de la outbox de la celda (compartido con mail-directory).
GRANT USAGE ON SCHEMA platform TO mail_service;
GRANT SELECT, INSERT, UPDATE, DELETE ON platform.event_outbox TO mail_service;
