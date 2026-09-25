-- Schema: access_control | Service: access-control
-- Familias de credencial (docs/adr/0017): la de envio manda correo y no gestiona credenciales; la de
-- aprovisionamiento crea empresas, dominios y claves de envio para otro producto de la casa y no puede
-- enviar ni leer un buzon. Las claves que ya existen son todas de envio.
ALTER TABLE access_control.api_keys
    ADD COLUMN IF NOT EXISTS kind varchar(20) NOT NULL DEFAULT 'sending';

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'api_keys_kind_check'
    ) THEN
        ALTER TABLE access_control.api_keys
            ADD CONSTRAINT api_keys_kind_check CHECK (kind IN ('sending', 'provisioning'));
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_api_keys_kind ON access_control.api_keys (kind, tenant_id);
