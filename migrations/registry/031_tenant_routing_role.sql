-- Schema: organization | Service: organization
--
-- Rol de ENRUTADO del registro. Un servicio de empresa atiende desde un solo despliegue a
-- todas las empresas de todas las celdas y, para saber en que base vive cada una, consulta
-- el registro. Hasta ahora lo hacia con la credencial de plataforma: la duena de TODAS las
-- bases del cluster, con la que se leen usuarios, permisos y facturacion. Un servicio de
-- empresa comprometido la tenia en su entorno.
--
-- Con esto el enrutado pasa a ser un privilegio propio: la vista publicada
-- organization.v_tenant_routing y un rol que solo puede leerla. Ni las tablas del registro,
-- ni identity, ni access_control, ni la outbox de plataforma.
--
-- Dos roles, como en la celda: un grupo NOLOGIN (tenant_router) que es donde viven los
-- permisos, y un rol de login miembro de el (mail_router) que crea
-- ops/db/tenant-service-role.sh con la contrasena del almacen. Asi ningun permiso depende
-- de que un script acierte a repetir la lista: el grupo la tiene una sola vez, aqui.
--
-- Idempotente y aditiva.

-- Vista publicada del enrutado: lo justo para abrir el pool de una empresa. Sin settings ni
-- ninguna columna de negocio. Se publica db_name, db_host y db_port porque son EL dato del
-- enrutado; quien lee esta vista es el que va a abrir esa conexion.
--
-- Sin CREATE OR REPLACE desnudo: una migracion posterior que le anada una columna dejaria
-- esta sin poder re-ejecutarse (42P16). La guarda la salta si la vista ya esta al dia.
DO $mig$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
         WHERE table_schema = 'organization' AND table_name = 'v_tenant_routing'
           AND column_name = 'cell_code'
    ) THEN
        EXECUTE $vista$
            CREATE OR REPLACE VIEW organization.v_tenant_routing AS
                SELECT t.id AS tenant_id, t.slug, t.status, t.db_name,
                       c.code AS cell_code, c.db_host, c.db_port
                  FROM organization.tenants t
                  LEFT JOIN organization.cells c ON c.id = t.cell_id
        $vista$;
    END IF;
END $mig$;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'tenant_router') THEN
        CREATE ROLE tenant_router NOLOGIN;
    END IF;
END $$;

-- Solo la vista. USAGE sobre el esquema no da acceso a ninguna tabla por si mismo: hace
-- falta el SELECT, y aqui solo se concede sobre v_tenant_routing.
GRANT USAGE ON SCHEMA organization TO tenant_router;
GRANT SELECT ON organization.v_tenant_routing TO tenant_router;

-- CONNECT a la base de registro para el grupo, que es donde esta el rol de login. Se
-- concede sobre la base actual: esta migracion solo corre en el registro, y asi una
-- restauracion o un cluster nuevo lo tienen sin un paso manual que recordar.
DO $$
BEGIN
    EXECUTE format('GRANT CONNECT ON DATABASE %I TO tenant_router', current_database());
END $$;
