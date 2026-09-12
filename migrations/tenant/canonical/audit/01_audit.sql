-- Schema: audit | Service: audit
-- Bitacora de auditoria del tenant: rastro de acciones (audit_logs) y registro de
-- cambios a nivel de fila (record_log) para las tablas que cada servicio decida
-- vigilar. Idempotente: se puede reejecutar sobre cualquier base de tenant.

CREATE SCHEMA IF NOT EXISTS audit;

-- Rastro de acciones. Lo escribe solo el servicio audit (eventos del bus y
-- rastro del API que publica el gateway); el repositorio no expone UPDATE ni
-- DELETE, y la cadena de hash de la migracion 03 detecta cualquier alteracion
-- hecha por fuera.
CREATE TABLE IF NOT EXISTS audit.audit_logs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    user_id uuid NOT NULL,
    session_id uuid,
    action varchar(100) NOT NULL,
    module varchar(100) NOT NULL,
    resource text NOT NULL,
    resource_id text,
    ip_address varchar(45) NOT NULL DEFAULT '0.0.0.0',
    user_agent text,
    request_id varchar(100),
    before_data jsonb,
    after_data jsonb,
    changes jsonb,
    severity varchar(20) NOT NULL DEFAULT 'info',
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT audit_logs_severity_check CHECK (severity IN ('info', 'warning', 'critical'))
);

CREATE INDEX IF NOT EXISTS idx_audit_tenant ON audit.audit_logs (tenant_id, created_at DESC);
-- Historial por usuario y accion: base del detector de seguridad (IP nueva,
-- dispositivo nuevo, viaje imposible).
CREATE INDEX IF NOT EXISTS idx_audit_user_action ON audit.audit_logs (user_id, action, created_at DESC);
-- Conteo de fallos por IP en ventana: deteccion de fuerza bruta.
CREATE INDEX IF NOT EXISTS idx_audit_action_ip ON audit.audit_logs (action, ip_address, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_resource ON audit.audit_logs (resource, resource_id);

-- Registro de cambios fila a fila, alimentado por el trigger audit.log_change().
-- Ningun servicio lo escribe desde codigo: cada servicio engancha el trigger a las
-- tablas que quiera vigilar en su propia migracion, con guarda to_regclass.
CREATE TABLE IF NOT EXISTS audit.record_log (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    table_schema varchar(63) NOT NULL,
    table_name varchar(63) NOT NULL,
    operation varchar(10) NOT NULL,
    user_id uuid,
    tenant_id uuid,
    record_id uuid,
    old_data jsonb,
    new_data jsonb,
    changed_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT record_log_operation_check CHECK (operation IN ('INSERT', 'UPDATE', 'DELETE'))
);

CREATE INDEX IF NOT EXISTS idx_audit_record_log_changed ON audit.record_log (changed_at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_record_log_table ON audit.record_log (table_schema, table_name);
CREATE INDEX IF NOT EXISTS idx_audit_record_log_tenant ON audit.record_log (tenant_id);
CREATE INDEX IF NOT EXISTS idx_audit_record_log_user ON audit.record_log (user_id);

-- Usuario y tenant salen de los GUC que fija la capa de acceso a datos por
-- transaccion (app.current_user_id, app.current_tenant_id); si faltan, el cambio
-- se registra igual con esos campos a NULL: la bitacora nunca bloquea la escritura.
CREATE OR REPLACE FUNCTION audit.log_change()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
DECLARE
    v_user_id   UUID;
    v_tenant_id UUID;
    v_record_id UUID;
BEGIN
    BEGIN
        v_user_id := current_setting('app.current_user_id', true)::UUID;
    EXCEPTION WHEN OTHERS THEN
        v_user_id := NULL;
    END;
    BEGIN
        v_tenant_id := current_setting('app.current_tenant_id', true)::UUID;
    EXCEPTION WHEN OTHERS THEN
        v_tenant_id := NULL;
    END;

    IF TG_OP = 'DELETE' THEN
        BEGIN v_record_id := (OLD.id)::UUID; EXCEPTION WHEN OTHERS THEN v_record_id := NULL; END;
        INSERT INTO audit.record_log (table_schema, table_name, operation, user_id, tenant_id, record_id, old_data, new_data)
        VALUES (TG_TABLE_SCHEMA, TG_TABLE_NAME, TG_OP, v_user_id, v_tenant_id, v_record_id, row_to_json(OLD)::JSONB, NULL);
        RETURN OLD;
    ELSIF TG_OP = 'UPDATE' THEN
        BEGIN v_record_id := (NEW.id)::UUID; EXCEPTION WHEN OTHERS THEN v_record_id := NULL; END;
        INSERT INTO audit.record_log (table_schema, table_name, operation, user_id, tenant_id, record_id, old_data, new_data)
        VALUES (TG_TABLE_SCHEMA, TG_TABLE_NAME, TG_OP, v_user_id, v_tenant_id, v_record_id, row_to_json(OLD)::JSONB, row_to_json(NEW)::JSONB);
        RETURN NEW;
    ELSIF TG_OP = 'INSERT' THEN
        BEGIN v_record_id := (NEW.id)::UUID; EXCEPTION WHEN OTHERS THEN v_record_id := NULL; END;
        INSERT INTO audit.record_log (table_schema, table_name, operation, user_id, tenant_id, record_id, old_data, new_data)
        VALUES (TG_TABLE_SCHEMA, TG_TABLE_NAME, TG_OP, v_user_id, v_tenant_id, v_record_id, NULL, row_to_json(NEW)::JSONB);
        RETURN NEW;
    END IF;
END;
$$;
