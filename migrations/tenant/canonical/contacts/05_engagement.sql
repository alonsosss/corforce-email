-- Schema: contacts | Service: contacts
--
-- Proyeccion de interaccion por contacto: una fila por contacto y envio de marketing
-- (campana, o flujo de automations, que transactional atribuye con campaign_id) con la hora
-- en que lo recibio y las de su ultima apertura y su ultimo clic. La mantiene el consumidor
-- de transactional.email.delivered, .opened y .clicked, y la leen las reglas de
-- comportamiento de los segmentos (internal/segment). Solo ids y horas: ni la direccion, ni
-- el enlace, ni la ip del evento.
--
-- El consumidor es idempotente sin tabla de eventos procesados: guarda la recepcion mas
-- antigua y la apertura y el clic mas recientes (LEAST/GREATEST), asi que reaplicar un
-- evento, o aplicarlos desordenados, deja la misma fila. last_event_at es la mas reciente de
-- las tres y gobierna la poda (CONTACTS_ENGAGEMENT_RETENTION_DAYS).
--
-- La fila cae con el contacto (ON DELETE CASCADE): borrar a una persona borra su rastro.
--
-- Idempotente y aditiva.

CREATE TABLE IF NOT EXISTS contacts.engagement (
    tenant_id uuid NOT NULL,
    contact_id uuid NOT NULL REFERENCES contacts.contacts (id) ON DELETE CASCADE,
    campaign_id uuid NOT NULL,
    received_at timestamptz NOT NULL,
    last_opened_at timestamptz,
    last_clicked_at timestamptz,
    last_event_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (contact_id, campaign_id)
);

-- Las ultimas N campanas de un contacto.
CREATE INDEX IF NOT EXISTS idx_contacts_engagement_recent
    ON contacts.engagement (contact_id, received_at DESC);
-- Quien abrio o hizo clic en una campana concreta.
CREATE INDEX IF NOT EXISTS idx_contacts_engagement_campaign_opened
    ON contacts.engagement (campaign_id, contact_id) WHERE last_opened_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_contacts_engagement_campaign_clicked
    ON contacts.engagement (campaign_id, contact_id) WHERE last_clicked_at IS NOT NULL;
-- Quien abrio o hizo clic en los ultimos N dias.
CREATE INDEX IF NOT EXISTS idx_contacts_engagement_opened_at
    ON contacts.engagement (tenant_id, last_opened_at) WHERE last_opened_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_contacts_engagement_clicked_at
    ON contacts.engagement (tenant_id, last_clicked_at) WHERE last_clicked_at IS NOT NULL;
-- Poda por antiguedad.
CREATE INDEX IF NOT EXISTS idx_contacts_engagement_last_event
    ON contacts.engagement (tenant_id, last_event_at);

DO $$
BEGIN
    CREATE TRIGGER trg_contacts_engagement_updated
        BEFORE UPDATE ON contacts.engagement
        FOR EACH ROW EXECUTE FUNCTION update_updated_at();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;
