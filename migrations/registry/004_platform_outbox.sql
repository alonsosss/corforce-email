-- Schema: platform | Service: platform
--
-- Outbox de eventos (pkg/outbox): el evento se escribe en la misma transaccion que el
-- dato y un rele lo publica despues en JetStream. Existe en cada base (registro, celda y
-- empresa) con la misma forma; la posee la plataforma, no un servicio: todos encolan y el
-- rele de cada servicio vacia lo suyo por subject.
CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE SCHEMA IF NOT EXISTS platform;

CREATE TABLE IF NOT EXISTS platform.event_outbox (
    id           uuid PRIMARY KEY,
    subject      text NOT NULL,
    tenant_id    uuid,
    payload      jsonb NOT NULL,
    attempts     integer NOT NULL DEFAULT 0,
    last_error   text,
    created_at   timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz
);

-- Solo lo pendiente se recorre; lo publicado se poda por antiguedad.
CREATE INDEX IF NOT EXISTS idx_platform_event_outbox_pending
    ON platform.event_outbox (created_at) WHERE published_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_platform_event_outbox_published
    ON platform.event_outbox (published_at) WHERE published_at IS NOT NULL;
