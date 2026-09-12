-- Schema: identity | Service: identity
--
-- Esquema del servicio de identidad en la base de registro: usuarios, sesiones,
-- politicas de contrasena y de sesion, historial, recuperacion de contrasena, lista de
-- bloqueo de access tokens y bitacora propia. Idempotente: se puede re-ejecutar.
--
-- tenant_id nunca lleva clave foranea a organization: sin claves entre esquemas. La
-- funcion update_updated_at() y pgcrypto (gen_random_uuid) las crea 001_organization.sql.

CREATE SCHEMA IF NOT EXISTS identity;

-- Usuarios. Una cuenta pertenece a UNA empresa; el mismo correo puede existir en varias.
CREATE TABLE IF NOT EXISTS identity.users (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id             uuid NOT NULL,
    email                 varchar(255) NOT NULL,
    password_hash         varchar(255) NOT NULL,
    first_name            varchar(100) NOT NULL,
    last_name             varchar(100) NOT NULL,
    -- Foto de perfil (data URL o URL). Para la pagina Mi Perfil.
    avatar_url            text,
    status                varchar(20) NOT NULL DEFAULT 'active'
                          CHECK (status IN ('active', 'inactive', 'locked', 'pending')),
    mfa_enabled           boolean NOT NULL DEFAULT false,
    mfa_secret            varchar(255),
    password_changed_at   timestamptz,
    failed_login_attempts integer NOT NULL DEFAULT 0,
    locked_until          timestamptz,
    last_login_at         timestamptz,
    -- Epoch de revocacion: al cerrar todas las sesiones o cambiar la contrasena se pone a
    -- now() y el gateway rechaza cualquier access token emitido ANTES (iat anterior).
    -- Un token robado o de una sesion revocada deja de servir en segundos, sin esperar a
    -- que caduque. to_timestamp(0) deja validos los ya emitidos hasta la primera revocacion.
    tokens_valid_from     timestamptz NOT NULL DEFAULT to_timestamp(0),
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, email)
);

CREATE INDEX IF NOT EXISTS idx_identity_users_tenant ON identity.users (tenant_id);
CREATE INDEX IF NOT EXISTS idx_identity_users_email  ON identity.users (email);
CREATE INDEX IF NOT EXISTS idx_identity_users_status ON identity.users (status);

DO $$
BEGIN
    CREATE TRIGGER trg_identity_users_updated_at
        BEFORE UPDATE ON identity.users
        FOR EACH ROW EXECUTE FUNCTION update_updated_at();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- Sesiones: una fila por refresh token. La rotacion crea una fila nueva y revoca la
-- anterior, asi que created_at es la ultima renovacion y login_at el inicio original.
CREATE TABLE IF NOT EXISTS identity.sessions (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id            uuid NOT NULL REFERENCES identity.users (id) ON DELETE CASCADE,
    refresh_token_hash varchar(255) NOT NULL UNIQUE,
    ip_address         inet,
    user_agent         text,
    expires_at         timestamptz NOT NULL,
    revoked            boolean NOT NULL DEFAULT false,
    -- Marca temporal de la revocacion: habilita la gracia anti-carrera de la rotacion
    -- (varias copias del cliente renovando a la vez con el mismo token).
    revoked_at         timestamptz,
    -- 'rotated' = revocada por la rotacion normal del refresh (con gracia);
    -- 'revoked' = cierre deliberado (logout, logout-all, revocacion de admin): sin gracia.
    revoked_reason     varchar(20),
    -- Inicio de sesion original; se propaga por la cadena de rotacion.
    login_at           timestamptz,
    created_at         timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_identity_sessions_user    ON identity.sessions (user_id);
CREATE INDEX IF NOT EXISTS idx_identity_sessions_expires ON identity.sessions (expires_at);
CREATE INDEX IF NOT EXISTS idx_identity_sessions_active
    ON identity.sessions (user_id, created_at DESC) WHERE revoked = false;

-- Access tokens bloqueados por logout hasta que caduquen.
CREATE TABLE IF NOT EXISTS identity.token_blocklist (
    token_hash varchar(255) PRIMARY KEY,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_identity_token_blocklist_expires ON identity.token_blocklist (expires_at);

-- Politica de contrasenas por empresa. Sin fila rige la politica por defecto del
-- dominio, que coincide con estos DEFAULT.
CREATE TABLE IF NOT EXISTS identity.password_policies (
    tenant_id                uuid PRIMARY KEY,
    min_length               integer NOT NULL DEFAULT 8,
    require_uppercase        boolean NOT NULL DEFAULT true,
    require_lowercase        boolean NOT NULL DEFAULT true,
    require_digit            boolean NOT NULL DEFAULT true,
    require_special          boolean NOT NULL DEFAULT true,
    max_age_days             integer NOT NULL DEFAULT 90,
    history_count            integer NOT NULL DEFAULT 5,
    max_failed_attempts      integer NOT NULL DEFAULT 5,
    lockout_duration_minutes integer NOT NULL DEFAULT 30
);

-- Historial de hashes para impedir reutilizar las ultimas N contrasenas.
CREATE TABLE IF NOT EXISTS identity.password_history (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id       uuid NOT NULL REFERENCES identity.users (id) ON DELETE CASCADE,
    password_hash varchar(255) NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_identity_password_history_user ON identity.password_history (user_id);

-- Tokens de recuperacion de contrasena ("olvide mi contrasena"). Se guarda solo el hash
-- SHA-256; el token en claro viaja una unica vez en el enlace del correo. Un solo uso y
-- con expiracion corta.
CREATE TABLE IF NOT EXISTS identity.password_reset_tokens (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL REFERENCES identity.users (id) ON DELETE CASCADE,
    tenant_id  uuid NOT NULL,
    token_hash varchar(64) NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,
    used_at    timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_identity_password_reset_tokens_user
    ON identity.password_reset_tokens (user_id);

-- Politica de sesion por empresa. Los tres controles son opt-in: 0 significa "sin
-- limite" y sin fila rige la politica por defecto del dominio.
CREATE TABLE IF NOT EXISTS identity.session_policies (
    tenant_id               uuid PRIMARY KEY,
    -- Vida del refresh token: cuanto puede durar una sesion sin volver a autenticarse.
    refresh_ttl_hours       integer NOT NULL DEFAULT 168,
    -- Sesiones activas simultaneas por usuario; al superarlo se cierra la mas antigua.
    max_concurrent_sessions integer NOT NULL DEFAULT 0,
    -- Minutos sin actividad tras los cuales la sesion deja de renovarse.
    idle_timeout_minutes    integer NOT NULL DEFAULT 0,
    updated_at              timestamptz NOT NULL DEFAULT now(),
    updated_by              uuid,
    CONSTRAINT session_policies_refresh_ttl_check  CHECK (refresh_ttl_hours BETWEEN 1 AND 8760),
    CONSTRAINT session_policies_max_sessions_check CHECK (max_concurrent_sessions BETWEEN 0 AND 100),
    CONSTRAINT session_policies_idle_check         CHECK (idle_timeout_minutes BETWEEN 0 AND 43200)
);

-- Bitacora propia de identity: logins, bloqueos, reinicios, cambios de politica y
-- revocaciones. Es el rastro local; el registro de seguridad del tenant lo alimentan
-- los eventos identity.* que consume el servicio audit.
CREATE TABLE IF NOT EXISTS identity.audit_log (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid NOT NULL,
    user_id     uuid,
    action      varchar(100) NOT NULL,
    resource    varchar(100),
    resource_id varchar(255),
    ip_address  inet,
    user_agent  text,
    details     jsonb,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_identity_audit_tenant  ON identity.audit_log (tenant_id);
CREATE INDEX IF NOT EXISTS idx_identity_audit_user    ON identity.audit_log (user_id);
CREATE INDEX IF NOT EXISTS idx_identity_audit_action  ON identity.audit_log (action);
CREATE INDEX IF NOT EXISTS idx_identity_audit_created ON identity.audit_log (created_at);
