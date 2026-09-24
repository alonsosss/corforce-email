-- Schema: access_control | Service: access-control
--
-- Claves de API por empresa (docs/adr/0013-claves-de-api-y-relay-smtp.md, oleada 3-G de
-- docs/Plan_Marketing_Avanzado.md). Una clave autentica una integracion, no a una persona: el
-- gateway la acepta solo en las rutas de envio que declara services/gateway/routes.json
-- (api_key_routes) y smtp-relay la usa como contrasena SMTP.
--
--   cfm_<prefix>_<secreto>
--
-- prefix es publico (se muestra en la lista y es el usuario SMTP); el secreto se muestra una sola
-- vez al crearla y aqui solo queda su HMAC-SHA256 con la llave API_KEY_HASH_KEY del almacen
-- (hash_key_id identifica la llave, pkg/crypto.MACKeyRing). Una fuga de esta tabla no permite
-- probar secretos sin esa llave.
--
-- El alcance son permisos exactos del catalogo marcados api_key_grantable, y nunca mas de lo que
-- tiene quien la crea. Al usarla, access-control lo vuelve a acotar a lo que esa persona tiene en
-- ese momento: si pierde el permiso o su cuenta deja de estar activa, la clave deja de servir.
--
-- Una sola migracion con las tablas y los permisos: el numero 044 es el reservado a 3-G en el
-- registro y un segundo numero chocaria con las demas oleadas. Idempotente y aditiva.
--   api_keys/read    ver las claves de la empresa (nunca su secreto)
--   api_keys/create  crear una clave con parte de los permisos propios
--   api_keys/revoke  revocar una clave

ALTER TABLE access_control.permissions
    ADD COLUMN IF NOT EXISTS api_key_grantable boolean NOT NULL DEFAULT false;

INSERT INTO access_control.permissions (module, resource, action, description, scope) VALUES
('access', 'api_keys', 'read',   'Ver las claves de API de la empresa', 'tenant'),
('access', 'api_keys', 'create', 'Crear claves de API y credenciales SMTP', 'tenant'),
('access', 'api_keys', 'revoke', 'Revocar claves de API', 'tenant')
ON CONFLICT (module, resource, action) DO NOTHING;

-- Lo que una clave puede llevar: el envio transaccional y la lectura de su estado. Ampliar la
-- lista es una migracion posterior y una ruta nueva en api_key_routes del gateway.
UPDATE access_control.permissions
   SET api_key_grantable = true
 WHERE module = 'transactional'
   AND resource = 'messages'
   AND action IN ('create', 'read')
   AND api_key_grantable = false;

CREATE TABLE IF NOT EXISTS access_control.api_keys (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    uuid NOT NULL,
    name         varchar(100) NOT NULL,
    prefix       varchar(16) NOT NULL,
    secret_hash  bytea NOT NULL,
    hash_key_id  varchar(32) NOT NULL,
    created_by   uuid NOT NULL,
    expires_at   timestamptz,
    revoked_at   timestamptz,
    revoked_by   uuid,
    last_used_at timestamptz,
    last_used_ip varchar(45),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT api_keys_prefix_key UNIQUE (prefix),
    CONSTRAINT api_keys_name_check CHECK (length(btrim(name)) > 0),
    CONSTRAINT api_keys_expiry_check CHECK (expires_at IS NULL OR expires_at > created_at)
);

CREATE INDEX IF NOT EXISTS idx_api_keys_tenant_created
    ON access_control.api_keys (tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_api_keys_created_by
    ON access_control.api_keys (tenant_id, created_by);

DROP TRIGGER IF EXISTS trg_api_keys_updated_at ON access_control.api_keys;
CREATE TRIGGER trg_api_keys_updated_at
    BEFORE UPDATE ON access_control.api_keys
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();

CREATE TABLE IF NOT EXISTS access_control.api_key_scopes (
    api_key_id    uuid NOT NULL REFERENCES access_control.api_keys (id) ON DELETE CASCADE,
    permission_id uuid NOT NULL REFERENCES access_control.permissions (id) ON DELETE CASCADE,
    PRIMARY KEY (api_key_id, permission_id)
);

CREATE INDEX IF NOT EXISTS idx_api_key_scopes_permission
    ON access_control.api_key_scopes (permission_id);
