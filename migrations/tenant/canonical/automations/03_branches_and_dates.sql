-- Schema: automations | Service: automations
--
-- Flujos como grafo acotado con ramas y disparadores por fecha.
--
-- * steps pasa de lista lineal a grafo sin ciclos (hasta 40 pasos): cada paso lleva su id y
--   sus destinos (next; then y else en una rama). Lo valida el dominio. Un flujo guardado
--   antes no tiene ids: el servicio lo lee como la cadena s1 -> s2 -> ... y las ejecuciones
--   en curso siguen apuntando al paso por su posicion (step_index), asi que no se toca ningun
--   dato.
-- * contact.date: aniversario (dia y mes) de un atributo de fecha del contacto a una hora
--   local, con una zona de respaldo para quien no tiene la suya. trigger_attribute,
--   trigger_hour y trigger_timezone existen solo con ese disparador.
-- * run_messages: el correo que envio cada paso de una ejecucion, con su apertura y su clic,
--   para las ramas que miran un correo anterior del flujo. Solo ids y horas.
-- * date_scans: el ultimo recorrido de aniversarios de cada flujo, que reparte el trabajo
--   entre replicas.
--
-- Idempotente y aditiva: las columnas y tablas nuevas se crean si faltan, y los CHECK que
-- cambian admiten todo lo que admitian (un disparador mas, hasta 40 pasos en lugar de 20).

ALTER TABLE automations.workflows ADD COLUMN IF NOT EXISTS trigger_attribute varchar(63);
ALTER TABLE automations.workflows ADD COLUMN IF NOT EXISTS trigger_hour smallint;
ALTER TABLE automations.workflows ADD COLUMN IF NOT EXISTS trigger_timezone varchar(64);

ALTER TABLE automations.workflows DROP CONSTRAINT IF EXISTS automations_workflows_trigger_check;
ALTER TABLE automations.workflows ADD CONSTRAINT automations_workflows_trigger_check CHECK (
    trigger_type IN ('contact.created', 'consent.granted', 'email.clicked', 'contact.date')
);

ALTER TABLE automations.workflows DROP CONSTRAINT IF EXISTS automations_workflows_steps_check;
ALTER TABLE automations.workflows ADD CONSTRAINT automations_workflows_steps_check CHECK (
    jsonb_typeof(steps) = 'array' AND jsonb_array_length(steps) BETWEEN 1 AND 40
);

ALTER TABLE automations.workflows DROP CONSTRAINT IF EXISTS automations_workflows_trigger_date_check;
ALTER TABLE automations.workflows ADD CONSTRAINT automations_workflows_trigger_date_check CHECK (
    (trigger_type = 'contact.date') = (trigger_attribute IS NOT NULL AND trigger_hour IS NOT NULL AND trigger_timezone IS NOT NULL)
    AND (trigger_hour IS NULL OR trigger_hour BETWEEN 0 AND 23)
);

CREATE TABLE IF NOT EXISTS automations.run_messages (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    run_id uuid NOT NULL REFERENCES automations.runs (id) ON DELETE CASCADE,
    workflow_id uuid NOT NULL,
    contact_id uuid NOT NULL,
    step_id varchar(32) NOT NULL,
    message_id uuid NOT NULL,
    opened_at timestamptz,
    clicked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT automations_run_messages_step_key UNIQUE (run_id, step_id),
    CONSTRAINT automations_run_messages_message_key UNIQUE (message_id)
);

CREATE INDEX IF NOT EXISTS idx_automations_run_messages_workflow
    ON automations.run_messages (workflow_id, created_at DESC);

DO $$
BEGIN
    CREATE TRIGGER trg_automations_run_messages_updated
        BEFORE UPDATE ON automations.run_messages
        FOR EACH ROW EXECUTE FUNCTION update_updated_at();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

CREATE TABLE IF NOT EXISTS automations.date_scans (
    workflow_id uuid PRIMARY KEY REFERENCES automations.workflows (id) ON DELETE CASCADE,
    tenant_id uuid NOT NULL,
    scanned_at timestamptz NOT NULL
);
