-- Schema: mail | Service: mail-directory
--
-- Rol mail_service: lo que los servicios de la celda hacen SIN empresa en la peticion
-- (mail-auth al verificar una contrasena, los mapas y la tuberia de los motores en
-- mail-security, la ruta interna de activacion, las tareas de fondo y el rele de la
-- outbox). Hasta ahora eso corria como dueno de las tablas, exento de RLS. Con la
-- credencial propia de la celda (rol de login <base>_svc, ops/db/cell-service-role.sh) el
-- servicio deja de ser dueno y necesita estos permisos explicitos.
--
-- Solo DML: sin DDL, sin TRUNCATE y sin ser dueno. La politica service_all deja pasar
-- todas las filas porque esos caminos resuelven identidades de cualquier empresa de la
-- celda y filtran por tenant_id en su SQL, como hasta ahora. Las peticiones con usuario
-- siguen cambiando a mail_app (pkg/db.TransactRLS), que no hereda nada de mail_service.
--
-- Toda tabla nueva con RLS en el esquema mail lleva su politica service_all en la misma
-- migracion que la crea; esta se la da a las existentes. Idempotente.

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'mail_service') THEN
        CREATE ROLE mail_service NOLOGIN;
    END IF;
END $$;

GRANT USAGE ON SCHEMA mail TO mail_service;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA mail TO mail_service;
ALTER DEFAULT PRIVILEGES IN SCHEMA mail GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO mail_service;

DO $$
DECLARE t text;
BEGIN
    FOR t IN
        SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
         WHERE n.nspname = 'mail' AND c.relkind IN ('r', 'p') AND c.relrowsecurity
    LOOP
        EXECUTE format('DROP POLICY IF EXISTS service_all ON mail.%I', t);
        EXECUTE format(
            'CREATE POLICY service_all ON mail.%I FOR ALL TO mail_service USING (true) WITH CHECK (true)', t);
    END LOOP;
END $$;

-- El rele de la outbox de la celda lee lo pendiente, marca lo publicado y poda.
GRANT USAGE ON SCHEMA platform TO mail_service;
GRANT SELECT, INSERT, UPDATE, DELETE ON platform.event_outbox TO mail_service;
