-- Schema: audit | Service: audit
-- Verificaciones de la cadena de hash en segundo plano (docs/adr/0006, "Verificacion asincrona").
--
-- Recorrer una cadena de millones de filas no cabe en una peticion. Cada verificacion es una fila
-- aqui: quien la lanzo, en que estado esta, hasta donde llego (el punto de reanudacion de cada
-- cadena: posicion y hash de la ultima fila verificada) y su resultado. Si el proceso que la corre
-- muere, otro la retoma desde ese punto cuando su latido (heartbeat_at) deja de avanzar.
--
-- El indice unico parcial es el cerrojo entre procesos: una empresa no puede tener dos
-- verificaciones en curso, ni con varias replicas de audit.
--
-- Lo que el rol del servicio escribe se limita como en el resto del esquema: inserta y lee, y solo
-- actualiza las columnas de estado de una verificacion en curso; nunca borra. La fila es el
-- registro de quien verifico y con que resultado.
--
-- Idempotente y aditiva. Corre despues de 04, que da los permisos amplios.

CREATE TABLE IF NOT EXISTS audit.integrity_runs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    mode text NOT NULL,
    origin text NOT NULL,
    requested_by uuid,
    status text NOT NULL DEFAULT 'running',
    phase text NOT NULL DEFAULT 'audit_logs',
    owner text NOT NULL,
    cancel_requested boolean NOT NULL DEFAULT false,
    target_seq bigint NOT NULL DEFAULT 0,
    current_seq bigint NOT NULL DEFAULT 0,
    checked_rows bigint NOT NULL DEFAULT 0,
    chains jsonb NOT NULL DEFAULT '{}'::jsonb,
    result jsonb,
    error_code text,
    started_at timestamptz NOT NULL DEFAULT now(),
    heartbeat_at timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    CONSTRAINT integrity_runs_mode_check CHECK (mode IN ('full', 'incremental')),
    CONSTRAINT integrity_runs_origin_check CHECK (origin IN ('manual', 'sweep', 'request')),
    CONSTRAINT integrity_runs_status_check CHECK (status IN ('running', 'completed', 'cancelled', 'failed')),
    CONSTRAINT integrity_runs_finished_check CHECK ((status = 'running') = (finished_at IS NULL))
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_integrity_runs_active ON audit.integrity_runs (tenant_id) WHERE status = 'running';
CREATE INDEX IF NOT EXISTS idx_integrity_runs_recent ON audit.integrity_runs (tenant_id, started_at DESC);
-- La ultima verificacion terminada y la ultima que dio la cadena por buena (punto de partida de la
-- incremental).
CREATE INDEX IF NOT EXISTS idx_integrity_runs_completed ON audit.integrity_runs (tenant_id, finished_at DESC)
    WHERE status = 'completed';

REVOKE UPDATE, DELETE, TRUNCATE ON audit.integrity_runs FROM audit_service;
GRANT SELECT, INSERT ON audit.integrity_runs TO audit_service;
GRANT UPDATE (owner, status, phase, cancel_requested, current_seq, checked_rows, chains, result, error_code, heartbeat_at, finished_at)
    ON audit.integrity_runs TO audit_service;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA audit TO audit_service;
