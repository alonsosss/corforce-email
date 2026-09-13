-- Schema: mail_security | Service: mail-security
--
-- Politicas antispam por empresa y por objeto (buzon o dominio), cuarentena y los datos
-- que alimentan los mapas dinamicos que Rspamd y Postfix piden por HTTP y las claves de
-- Redis que los motores leen.
--
-- Vive en la base de la CELDA porque Rspamd sirve a todas las empresas de la celda con
-- una sola configuracion (/settings devuelve UN documento UCL) y la cuarentena la escribe
-- el exportador de Rspamd sin saber de empresas: la fila se atribuye a la empresa del
-- buzon final al insertarla. El aislamiento es tenant_id en cada fila y RLS para el rol
-- de aplicacion (mail_app), con el mismo patron que el esquema mail.
--
-- Autosuficiente: no depende de que las migraciones de mail-directory se hayan aplicado
-- (redefine update_updated_at y crea mail_app si no existen). Idempotente y aditiva.

CREATE OR REPLACE FUNCTION update_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE SCHEMA IF NOT EXISTS mail_security;

-- ── Umbrales de spam por objeto ───────────────────────────────────────────────
-- object: un buzon (user@dominio) o un dominio de la empresa. high_score: a partir de
-- ahi Rspamd rechaza; low_score: a partir de ahi marca (add_header). Sin fila rigen los
-- umbrales globales de actions.conf.
CREATE TABLE IF NOT EXISTS mail_security.spam_scores (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid NOT NULL,
    object     text NOT NULL UNIQUE,
    high_score numeric(8,2) NOT NULL,
    low_score  numeric(8,2) NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT spam_scores_object_lower CHECK (object = lower(object)),
    CONSTRAINT spam_scores_order CHECK (low_score <= high_score)
);
CREATE INDEX IF NOT EXISTS idx_mail_security_spam_scores_tenant ON mail_security.spam_scores (tenant_id, object);

-- ── Listas blancas y negras por objeto ────────────────────────────────────────
-- pattern: direccion, '@dominio' o comodin con '*'. kind allow -> MAILCOW_WHITE,
-- deny -> MAILCOW_BLACK en el UCL de /settings (nombres que definen los ficheros de
-- Rspamd copiados).
CREATE TABLE IF NOT EXISTS mail_security.address_lists (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid NOT NULL,
    object     text NOT NULL,
    kind       text NOT NULL CHECK (kind IN ('allow', 'deny')),
    pattern    text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT address_lists_lower CHECK (object = lower(object) AND pattern = lower(pattern)),
    UNIQUE (object, kind, pattern)
);
CREATE INDEX IF NOT EXISTS idx_mail_security_address_lists_tenant ON mail_security.address_lists (tenant_id, object);

-- ── Bloques UCL adicionales por empresa ───────────────────────────────────────
-- content se pega tal cual dentro de settings { } cuando active. La validacion (sin
-- 'settings {' ni llaves desbalanceadas) la hace el servicio antes de guardar.
CREATE TABLE IF NOT EXISTS mail_security.settings_maps (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid NOT NULL,
    description text NOT NULL DEFAULT '',
    content     text NOT NULL,
    active      boolean NOT NULL DEFAULT true,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_mail_security_settings_maps_tenant ON mail_security.settings_maps (tenant_id);

-- ── Pie de pagina por dominio ─────────────────────────────────────────────────
-- Lo aplica Rspamd (MOO_FOOTER) al correo saliente autenticado del dominio.
-- mailbox_exclude: usuarios SASL sin pie; alias_domain_exclude: dominios alias del
-- remitente sin pie; skip_replies: no anadir el pie a respuestas (In-Reply-To).
CREATE TABLE IF NOT EXISTS mail_security.domain_footers (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id            uuid NOT NULL,
    domain               text NOT NULL UNIQUE,
    html                 text NOT NULL DEFAULT '',
    plain                text NOT NULL DEFAULT '',
    mailbox_exclude      jsonb NOT NULL DEFAULT '[]'::jsonb,
    alias_domain_exclude jsonb NOT NULL DEFAULT '[]'::jsonb,
    skip_replies         boolean NOT NULL DEFAULT false,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT domain_footers_domain_lower CHECK (domain = lower(domain))
);
CREATE INDEX IF NOT EXISTS idx_mail_security_domain_footers_tenant ON mail_security.domain_footers (tenant_id);

-- ── Hosts de reenvio de confianza ─────────────────────────────────────────────
-- host: IP o CIDR. filter_spam=false -> el spam que llega desde ese host no se filtra
-- (KEEP_SPAM en Redis). source: de donde salio el host (nombre DNS o nota).
CREATE TABLE IF NOT EXISTS mail_security.forwarding_hosts (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid NOT NULL,
    host        cidr NOT NULL UNIQUE,
    source      text NOT NULL DEFAULT '',
    filter_spam boolean NOT NULL DEFAULT true,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_mail_security_forwarding_hosts_tenant ON mail_security.forwarding_hosts (tenant_id);

-- ── Limites de envio por objeto ───────────────────────────────────────────────
-- value con el formato que entiende ratelimit.lua: "N / 1h" (s, m, h, d).
CREATE TABLE IF NOT EXISTS mail_security.rate_limits (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid NOT NULL,
    object     text NOT NULL UNIQUE,
    value      text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT rate_limits_object_lower CHECK (object = lower(object))
);
CREATE INDEX IF NOT EXISTS idx_mail_security_rate_limits_tenant ON mail_security.rate_limits (tenant_id, object);

-- ── Etiquetas (+tag) por buzon ────────────────────────────────────────────────
-- Como entregar el correo dirigido a local+tag@dominio: en el asunto o en subcarpeta.
CREATE TABLE IF NOT EXISTS mail_security.mailbox_tags (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     uuid NOT NULL,
    username      text NOT NULL UNIQUE,
    subject_tag   boolean NOT NULL DEFAULT false,
    subfolder_tag boolean NOT NULL DEFAULT false,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT mailbox_tags_username_lower CHECK (username = lower(username))
);
CREATE INDEX IF NOT EXISTS idx_mail_security_mailbox_tags_tenant ON mail_security.mailbox_tags (tenant_id, username);

-- ── Cuarentena ────────────────────────────────────────────────────────────────
-- Una fila por buzon final (rcpt) y mensaje. msg es el RFC822 crudo. qhash identifica
-- la fila en enlaces sin sesion (avisos). user_name: usuario SASL si el correo era
-- saliente autenticado. domain: dominio del buzon final, para excluir y agrupar.
CREATE TABLE IF NOT EXISTS mail_security.quarantine (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    uuid NOT NULL,
    qid          text NOT NULL DEFAULT '',
    subject      text NOT NULL DEFAULT '',
    score        numeric(10,4) NOT NULL DEFAULT 0,
    ip           inet,
    action       text NOT NULL DEFAULT '',
    symbols      jsonb NOT NULL DEFAULT '[]'::jsonb,
    fuzzy_hashes jsonb NOT NULL DEFAULT '[]'::jsonb,
    sender       text NOT NULL DEFAULT '',
    rcpt         text NOT NULL,
    msg          bytea NOT NULL,
    domain       text NOT NULL,
    notified     boolean NOT NULL DEFAULT false,
    user_name    text NOT NULL DEFAULT '',
    qhash        text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_mail_security_quarantine_tenant_created ON mail_security.quarantine (tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_mail_security_quarantine_tenant_rcpt ON mail_security.quarantine (tenant_id, rcpt, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_mail_security_quarantine_qhash ON mail_security.quarantine (qhash);
CREATE INDEX IF NOT EXISTS idx_mail_security_quarantine_created ON mail_security.quarantine (created_at);

-- ── Ajustes de cuarentena por empresa ─────────────────────────────────────────
-- Una fila por empresa. max_size_bytes: tamano maximo del mensaje que se guarda;
-- max_age_days: retencion; retention_size: filas por buzon; exclude_domains: dominios
-- cuyos buzones no se guardan; notify_*: aviso al usuario (plantilla HTML).
-- Redis (Q_*) recibe el TOPE de la celda (maximo entre empresas / union): la aplicacion
-- por empresa la hace el servicio con esta fila.
CREATE TABLE IF NOT EXISTS mail_security.quarantine_settings (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id            uuid NOT NULL UNIQUE,
    max_size_bytes       bigint NOT NULL DEFAULT 10485760 CHECK (max_size_bytes > 0),
    max_age_days         integer NOT NULL DEFAULT 365 CHECK (max_age_days > 0),
    retention_size       integer NOT NULL DEFAULT 100 CHECK (retention_size >= 0),
    exclude_domains      jsonb NOT NULL DEFAULT '[]'::jsonb,
    notify_enabled       boolean NOT NULL DEFAULT false,
    notify_max_score     numeric(8,2) NOT NULL DEFAULT 9999,
    notify_sender        text NOT NULL DEFAULT '',
    notify_subject       text NOT NULL DEFAULT '',
    notify_html_template text NOT NULL DEFAULT '',
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now()
);

-- ── Triggers de updated_at ────────────────────────────────────────────────────
DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['spam_scores','address_lists','settings_maps','domain_footers',
        'forwarding_hosts','rate_limits','mailbox_tags','quarantine_settings']
    LOOP
        IF NOT EXISTS (
            SELECT 1 FROM pg_trigger
             WHERE tgname = 'trg_mail_security_' || t || '_updated_at'
               AND tgrelid = ('mail_security.' || t)::regclass
        ) THEN
            EXECUTE format('CREATE TRIGGER trg_mail_security_%I_updated_at BEFORE UPDATE ON mail_security.%I FOR EACH ROW EXECUTE FUNCTION update_updated_at()', t, t);
        END IF;
    END LOOP;
END $$;

-- ── RLS: rol mail_app, politica por tenant_id ─────────────────────────────────
-- Mismo patron que mail: la aplicacion conecta como duena (exenta de RLS) y cambia a
-- mail_app con SET LOCAL ROLE dentro de la transaccion (pkg/db.TransactRLS). Los
-- endpoints que atienden a los motores (sin empresa en la peticion) consultan como
-- duena y filtran de forma explicita. Sin FORCE a proposito: pg_dump respalda todo.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'mail_app') THEN
        CREATE ROLE mail_app NOLOGIN;
    END IF;
END $$;

DO $$
BEGIN
    EXECUTE format('GRANT mail_app TO %I', current_user);
END $$;

GRANT USAGE ON SCHEMA mail_security TO mail_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA mail_security TO mail_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA mail_security GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO mail_app;

CREATE OR REPLACE FUNCTION mail_security.current_tenant() RETURNS uuid
LANGUAGE sql STABLE AS $$
    SELECT NULLIF(current_setting('app.current_tenant_id', true), '')::uuid
$$;

DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['spam_scores','address_lists','settings_maps','domain_footers',
        'forwarding_hosts','rate_limits','mailbox_tags','quarantine','quarantine_settings']
    LOOP
        EXECUTE format('ALTER TABLE mail_security.%I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON mail_security.%I', t);
        EXECUTE format(
            'CREATE POLICY tenant_isolation ON mail_security.%I FOR ALL TO mail_app '
            'USING (tenant_id = mail_security.current_tenant()) WITH CHECK (tenant_id = mail_security.current_tenant())', t);
    END LOOP;
END $$;
