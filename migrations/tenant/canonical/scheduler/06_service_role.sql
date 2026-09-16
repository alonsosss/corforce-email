-- Schema: scheduler | Service: scheduler
--
-- Rol de servicio del esquema scheduler en la base de CADA empresa. Hasta ahora scheduler abria
-- la base de toda empresa con la credencial de plataforma, que es duena de las tablas de
-- todos los servicios y ademas abre el registro: un fallo en un servicio alcanzaba lo que
-- guardan los demas. Con este rol, scheduler solo llega a scheduler.
--
-- Dos roles, el mismo patron que la celda (mail-directory/06_service_role.sql): el grupo
-- NOLOGIN scheduler_service tiene los permisos -que se enumeran AQUI, junto a las tablas que
-- los necesitan- y el rol de login mail_svc_scheduler, que crea ops/db/tenant-service-role.sh
-- con la contrasena del almacen, es miembro suyo. Ningun permiso depende de que un script
-- repita bien una lista.
--
-- Solo DML: ni DDL, ni TRUNCATE, ni dueno. Las migraciones siguen corriendo como dueno
-- (organization), asi que una tabla nueva de este servicio queda cubierta por el ALTER
-- DEFAULT PRIVILEGES de abajo sin tocar esta migracion.
--
-- Idempotente y aditiva.

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'scheduler_service') THEN
        CREATE ROLE scheduler_service NOLOGIN;
    END IF;
END $$;

GRANT USAGE ON SCHEMA scheduler TO scheduler_service;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA scheduler TO scheduler_service;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA scheduler TO scheduler_service;
ALTER DEFAULT PRIVILEGES IN SCHEMA scheduler
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO scheduler_service;
ALTER DEFAULT PRIVILEGES IN SCHEMA scheduler
    GRANT USAGE, SELECT ON SEQUENCES TO scheduler_service;

-- La outbox de la empresa: el evento se encola en la misma transaccion que el cambio y un
-- rele lo publica (pkg/outbox). Sin esto, Enqueue fallaria con permission denied y
-- revertiria la escritura de negocio. El rele ademas marca lo publicado y poda.
--
-- Bajo guarda porque el esquema platform lo crea platform/00_outbox.sql: el runner canonico
-- aplica los directorios en orden numerico y para cuando llega aqui siempre existe, pero una
-- prueba de integracion que aplica SOLO el directorio de su servicio no lo tiene, y sin la
-- guarda esa base se queda sin esta migracion y sin las siguientes.
DO $$
BEGIN
    IF to_regclass('platform.event_outbox') IS NOT NULL THEN
        EXECUTE 'GRANT USAGE ON SCHEMA platform TO scheduler_service';
        EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON platform.event_outbox TO scheduler_service';
    END IF;
END $$;

-- CONNECT sobre la base de la empresa en la que corre esta migracion. Es lo que hace que
-- una empresa nueva quede lista sin un paso aparte: organization crea su base, la cierra a
-- PUBLIC y le aplica las canonicas, y esta le da la entrada al rol de scheduler y a nadie mas.
DO $$
BEGIN
    EXECUTE format('GRANT CONNECT ON DATABASE %I TO scheduler_service', current_database());
END $$;
