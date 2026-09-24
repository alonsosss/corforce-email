-- Schema: analytics | Service: analytics
--
-- Rol de servicio del esquema analytics en la base de CADA empresa. Hasta ahora analytics abria
-- la base de toda empresa con la credencial de plataforma, que es duena de las tablas de
-- todos los servicios y ademas abre el registro: un fallo en un servicio alcanzaba lo que
-- guardan los demas. Con este rol, analytics solo llega a analytics.
--
-- Dos roles, el mismo patron que la celda (mail-directory/06_service_role.sql): el grupo
-- NOLOGIN analytics_service tiene los permisos -que se enumeran AQUI, junto a las tablas que
-- los necesitan- y el rol de login mail_svc_analytics, que crea ops/db/tenant-service-role.sh
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
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'analytics_service') THEN
        BEGIN
            CREATE ROLE analytics_service NOLOGIN;
        EXCEPTION WHEN duplicate_object OR unique_violation THEN
            -- Los roles son del cluster: otra base de empresa que migraba a la vez lo acaba de crear.
            NULL;
        END;
    END IF;
END $$;

GRANT USAGE ON SCHEMA analytics TO analytics_service;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA analytics TO analytics_service;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA analytics TO analytics_service;
ALTER DEFAULT PRIVILEGES IN SCHEMA analytics
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO analytics_service;
ALTER DEFAULT PRIVILEGES IN SCHEMA analytics
    GRANT USAGE, SELECT ON SEQUENCES TO analytics_service;

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
        EXECUTE 'GRANT USAGE ON SCHEMA platform TO analytics_service';
        EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON platform.event_outbox TO analytics_service';
    END IF;
END $$;

-- CONNECT sobre la base de la empresa en la que corre esta migracion. Es lo que hace que
-- una empresa nueva quede lista sin un paso aparte: organization crea su base, la cierra a
-- PUBLIC y le aplica las canonicas, y esta le da la entrada al rol de analytics y a nadie mas.
DO $$
BEGIN
    EXECUTE format('GRANT CONNECT ON DATABASE %I TO analytics_service', current_database());
END $$;
