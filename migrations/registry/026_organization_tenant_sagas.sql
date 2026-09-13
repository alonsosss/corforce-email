-- Schema: organization | Service: organization
--
-- Saga del alta y la baja de cada empresa. organization no escribe en identity ni en
-- access_control: les pide por su API interna el rol del sistema, el primer usuario, la
-- asignacion y su retirada, y aqui guarda en que paso va cada empresa para retomarla tras
-- una caida o deshacerla si falla. Una fila por empresa, que cae con ella.
--
-- step es el ultimo paso terminado mientras la saga avanza (running) y el mas alto que queda
-- por deshacer mientras compensa (compensating). El arriendo (lease_token, lease_until)
-- impide que dos instancias ejecuten la misma saga; sin arriendo vigente, el barrido de
-- organization la retoma. La contrasena del primer administrador no se guarda nunca.
-- Idempotente.

CREATE TABLE IF NOT EXISTS organization.tenant_sagas (
    tenant_id     uuid PRIMARY KEY REFERENCES organization.tenants (id) ON DELETE CASCADE,
    operation     varchar(10) NOT NULL CHECK (operation IN ('create', 'delete')),
    state         varchar(20) NOT NULL CHECK (state IN ('running', 'compensating', 'failed', 'completed')),
    step          varchar(40) NOT NULL,
    admin_user_id uuid,
    role_id       uuid,
    drop_database boolean NOT NULL DEFAULT false,
    attempts      integer NOT NULL DEFAULT 0,
    last_error    text NOT NULL DEFAULT '',
    lease_token   uuid,
    lease_until   timestamptz,
    created_at    timestamptz NOT NULL DEFAULT NOW(),
    updated_at    timestamptz NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_tenant_sagas_pending
    ON organization.tenant_sagas (updated_at) WHERE state IN ('running', 'compensating');

CREATE OR REPLACE TRIGGER trg_tenant_sagas_updated_at BEFORE UPDATE ON organization.tenant_sagas
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();
