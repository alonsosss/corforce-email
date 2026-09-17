-- Schema: audit | Service: audit
--
-- Diff campo a campo de un audit_log puntual (CompareChanges), separado de
-- audit.record_log: record_log es el rastro generico que alimenta el trigger
-- audit.log_change() sobre las tablas que cada servicio decida vigilar;
-- data_change_records es el detalle "que cambio exactamente" de una entrada de
-- audit_logs concreta (before_data/after_data ya comparados campo a campo), que
-- el handler expone bajo demanda en GET /audit/logs/{id}/changes.
--
-- tenant_id es NOT NULL a proposito, aunque ya se puede derivar via audit_log_id:
-- GetByLogID filtra por tenant_id ademas de audit_log_id para que la consulta
-- nunca dependa de un JOIN para aislar tenants. Idempotente.

CREATE TABLE IF NOT EXISTS audit.data_change_records (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    uuid NOT NULL,
    audit_log_id uuid NOT NULL REFERENCES audit.audit_logs (id),
    field_name   text NOT NULL,
    old_value    text,
    new_value    text,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_data_change_records_log ON audit.data_change_records (tenant_id, audit_log_id);
