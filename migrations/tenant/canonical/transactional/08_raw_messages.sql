-- Schema: transactional | Service: transactional
--
-- Mensajes crudos (MIME) que llegan por smtp-relay (docs/adr/0013-claves-de-api-y-relay-smtp.md):
-- salen por SES con contenido Raw, tal como los escribio la empresa salvo las cabeceras que la
-- plataforma no deja pasar (pkg/rawmail.Sanitize). El mensaje sigue siendo una fila de messages
-- (estado, eventos, supresion, reputacion y facturacion como cualquier otro) y el MIME va aparte
-- para no cargarlo en cada listado.
--
-- origin: api (el API JSON, tambien con clave de API) o smtp (smtp-relay).
-- api_key_id: la clave de API con la que se creo, si la hubo (nunca un secreto).
--
-- Idempotente y aditiva.

ALTER TABLE transactional.messages
    ADD COLUMN IF NOT EXISTS origin varchar(10) NOT NULL DEFAULT 'api';
ALTER TABLE transactional.messages
    ADD COLUMN IF NOT EXISTS api_key_id uuid;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
         WHERE conrelid = 'transactional.messages'::regclass AND conname = 'messages_origin_check'
    ) THEN
        ALTER TABLE transactional.messages
            ADD CONSTRAINT messages_origin_check CHECK (origin IN ('api', 'smtp'));
    END IF;
END $$;

-- Un mensaje de SMTP es siempre transaccional y nunca una prueba: el relay no tiene otra via.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
         WHERE conrelid = 'transactional.messages'::regclass AND conname = 'messages_smtp_class_check'
    ) THEN
        ALTER TABLE transactional.messages
            ADD CONSTRAINT messages_smtp_class_check CHECK (origin <> 'smtp' OR (class = 'transactional' AND NOT is_test));
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_transactional_messages_tenant_api_key
    ON transactional.messages (tenant_id, api_key_id, created_at DESC) WHERE api_key_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS transactional.raw_contents (
    message_id uuid PRIMARY KEY REFERENCES transactional.messages (id) ON DELETE CASCADE,
    tenant_id  uuid NOT NULL,
    content    bytea NOT NULL,
    size_bytes integer NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT raw_contents_size_check CHECK (size_bytes > 0 AND size_bytes = octet_length(content))
);

CREATE INDEX IF NOT EXISTS idx_transactional_raw_contents_tenant
    ON transactional.raw_contents (tenant_id, created_at);
