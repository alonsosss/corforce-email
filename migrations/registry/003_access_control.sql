-- Schema: access_control | Service: access-control
--
-- RBAC del plano de control: roles por tenant, catalogo global de permisos como triple
-- (module, resource, action), vinculo rol-permiso, asignacion usuario-rol y registro de
-- accesos denegados. Idempotente.
--
-- Sin claves foraneas a otros esquemas: tenant_id y user_id son identificadores que
-- viven en organization e identity, pero cada esquema se administra por su servicio.
-- La funcion update_updated_at() la crea 001_organization.sql.

CREATE SCHEMA IF NOT EXISTS access_control;

-- Roles: pertenecen a un tenant. Los estructurales (is_system) los siembra organization
-- al crear el tenant y no se pueden modificar ni borrar desde la API.
CREATE TABLE IF NOT EXISTS access_control.roles (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid NOT NULL,
    name        varchar(100) NOT NULL,
    description text NOT NULL DEFAULT '',
    is_system   boolean NOT NULL DEFAULT false,
    status      varchar(20) NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, name)
);

CREATE INDEX IF NOT EXISTS idx_roles_tenant ON access_control.roles (tenant_id);

DROP TRIGGER IF EXISTS trg_roles_updated_at ON access_control.roles;
CREATE TRIGGER trg_roles_updated_at
    BEFORE UPDATE ON access_control.roles
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();

-- Permisos: catalogo global de la plataforma, sembrado por migracion. resource y action
-- admiten el comodin '*'.
CREATE TABLE IF NOT EXISTS access_control.permissions (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    module      varchar(50) NOT NULL,
    resource    varchar(100) NOT NULL,
    action      varchar(50) NOT NULL,
    description text NOT NULL DEFAULT '',
    UNIQUE (module, resource, action)
);

CREATE INDEX IF NOT EXISTS idx_permissions_module ON access_control.permissions (module);

CREATE TABLE IF NOT EXISTS access_control.role_permissions (
    role_id       uuid NOT NULL REFERENCES access_control.roles (id) ON DELETE CASCADE,
    permission_id uuid NOT NULL REFERENCES access_control.permissions (id) ON DELETE CASCADE,
    PRIMARY KEY (role_id, permission_id)
);

-- Asignacion usuario-rol. La clave primaria es la identidad de replica: una replicacion
-- logica o un CDC sobre el registro puede emitir sus UPDATE y DELETE sin mas.
CREATE TABLE IF NOT EXISTS access_control.user_roles (
    user_id     uuid NOT NULL,
    role_id     uuid NOT NULL REFERENCES access_control.roles (id) ON DELETE CASCADE,
    assigned_at timestamptz NOT NULL DEFAULT now(),
    assigned_by uuid,
    PRIMARY KEY (user_id, role_id)
);

CREATE INDEX IF NOT EXISTS idx_user_roles_role ON access_control.user_roles (role_id);

-- Registro de accesos denegados por el control RBAC del gateway, para que la
-- administracion vea metricas (que modulos y usuarios chocan con permisos) sin leer
-- los logs. enforced distingue el bloqueo real de la simulacion en modo auditoria.
CREATE TABLE IF NOT EXISTS access_control.access_denials (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid NOT NULL,
    user_id    uuid NOT NULL,
    module     varchar(50) NOT NULL DEFAULT '',
    action     varchar(50) NOT NULL DEFAULT '',
    method     varchar(10) NOT NULL DEFAULT '',
    path       text NOT NULL DEFAULT '',
    enforced   boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_access_denials_tenant_time
    ON access_control.access_denials (tenant_id, created_at DESC);
