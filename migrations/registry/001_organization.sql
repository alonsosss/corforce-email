-- Schema: organization | Service: organization
--
-- Plano de control de la plataforma de correo: directorio de celdas, registro de
-- tenants y catalogo de modulos con su estado por tenant. Idempotente: se aplica en
-- cada arranque del servicio organization. Sin claves foraneas hacia otros esquemas.

CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE OR REPLACE FUNCTION update_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE SCHEMA IF NOT EXISTS organization;

-- Directorio de celdas: un Postgres por region donde viven las bases de un conjunto de
-- tenants. Solo describe donde esta cada celda; no se siembra ninguna aqui porque la
-- celda es un dato del despliegue y se crea por API o desde ops/.
CREATE TABLE IF NOT EXISTS organization.cells (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    code       text NOT NULL UNIQUE,
    region     text NOT NULL,
    status     text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'draining', 'closed')),
    db_host    text NOT NULL,
    db_port    integer NOT NULL CHECK (db_port BETWEEN 1 AND 65535),
    created_at timestamptz NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_cells_status ON organization.cells(status);

-- Registro de tenants: una base fisica por tenant, alojada en una celda.
CREATE TABLE IF NOT EXISTS organization.tenants (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slug       varchar(63) NOT NULL UNIQUE,
    name       varchar(255) NOT NULL,
    db_name    varchar(63) NOT NULL UNIQUE,
    status     varchar(20) NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended', 'inactive')),
    cell_id    uuid NOT NULL,
    settings   jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT NOW(),
    updated_at timestamptz NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_tenants_status ON organization.tenants(status);
CREATE INDEX IF NOT EXISTS idx_tenants_cell ON organization.tenants(cell_id);

CREATE OR REPLACE TRIGGER trg_tenants_updated_at BEFORE UPDATE ON organization.tenants
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();

-- Catalogo de modulos de producto. tier core = no se deshabilita; requires = dependencias
-- duras; permission_modules = modulos de permiso (access_control.permissions.module)
-- que agrupa cada modulo, para que access-control y el gateway lean la relacion de la
-- base y no de codigo.
CREATE TABLE IF NOT EXISTS organization.module_catalog (
    module             text PRIMARY KEY,
    tier               text NOT NULL DEFAULT 'optional' CHECK (tier IN ('core', 'optional')),
    requires           jsonb NOT NULL DEFAULT '[]',
    label              text NOT NULL DEFAULT '',
    permission_modules jsonb NOT NULL DEFAULT '[]'
);

-- Estado de habilitacion por tenant. La AUSENCIA de filas para un tenant significa
-- "todos los modulos habilitados".
CREATE TABLE IF NOT EXISTS organization.tenant_modules (
    tenant_id  uuid NOT NULL,
    module     text NOT NULL,
    enabled    boolean NOT NULL DEFAULT true,
    updated_at timestamptz NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, module)
);

CREATE INDEX IF NOT EXISTS idx_tenant_modules_tenant ON organization.tenant_modules(tenant_id);

CREATE OR REPLACE TRIGGER trg_tenant_modules_updated_at BEFORE UPDATE ON organization.tenant_modules
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();

INSERT INTO organization.module_catalog (module, tier, requires, label, permission_modules) VALUES
    ('corporate_mail', 'optional', '[]',                  'Correo corporativo',   '["domains","mailboxes","mail_routing","mail_security","mail_storage"]'),
    ('transactional',  'optional', '[]',                  'Correo transaccional', '["transactional","templates","suppression","reputation"]'),
    ('marketing',      'optional', '["transactional"]',   'Marketing',            '["contacts","segments","campaigns","automations","analytics"]')
ON CONFLICT (module) DO UPDATE
    SET tier = EXCLUDED.tier,
        requires = EXCLUDED.requires,
        label = EXCLUDED.label,
        permission_modules = EXCLUDED.permission_modules;
