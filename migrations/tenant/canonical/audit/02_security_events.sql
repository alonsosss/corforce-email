-- Schema: audit | Service: audit
-- Eventos de seguridad que el servicio audit persiste y expone en
-- /api/v1/audit/security-events: los levanta el detector (fuerza bruta, IP o
-- dispositivo nuevo, viaje imposible, cuenta bloqueada, sesion revocada) y los
-- que derivan de acciones marcadas como sensibles en la bitacora.
-- Idempotente: se puede reejecutar sobre cualquier base de tenant.

CREATE SCHEMA IF NOT EXISTS audit;

CREATE TABLE IF NOT EXISTS audit.security_events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    user_id uuid,
    event_type varchar(50) NOT NULL,
    -- inet valida la direccion al escribir; el servicio lee host(ip_address) para
    -- devolverla como texto.
    ip_address inet,
    user_agent text,
    -- Descripcion legible del hallazgo, no un documento: el detector escribe texto
    -- plano y ningun consumidor lo trata como JSON.
    detail text,
    risk_level varchar(20) NOT NULL DEFAULT 'low',
    acknowledged boolean NOT NULL DEFAULT false,
    acknowledged_by uuid,
    acknowledged_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT security_events_risk_level_check CHECK (risk_level IN ('low', 'medium', 'high', 'critical'))
);

CREATE INDEX IF NOT EXISTS idx_sec_events_tenant
    ON audit.security_events (tenant_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_sec_events_unacked
    ON audit.security_events (tenant_id, acknowledged)
    WHERE acknowledged = false;

-- Deduplicacion del detector: una alerta por tipo, IP y ventana, no una por intento.
CREATE INDEX IF NOT EXISTS idx_sec_events_type_ip
    ON audit.security_events (tenant_id, event_type, ip_address, created_at DESC);
