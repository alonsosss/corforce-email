-- Schema: mail | Service: mail-directory
--
-- Directorio de correo de la CELDA: dominios, buzones, aliases y politicas de
-- entrega que leen directamente Postfix (mapas pgsql:), Dovecot (userdb y dicts)
-- y los servicios Go de la plataforma.
--
-- Vive en la base de la celda y no en la base de cada empresa porque un motor
-- SMTP no puede consultar N bases: Postfix resuelve un destinatario con una sola
-- consulta y necesita ver todos los dominios que sirve. El aislamiento entre
-- empresas es por tenant_id en cada fila y por RLS para los servicios Go; los
-- motores entran con el rol mail_engine, que solo ve lo que sus consultas
-- necesitan y no puede escribir salvo el uso de cuota.
--
-- El modelo conserva la semantica del directorio de mailcow (que es la que los
-- motores entienden) traducida a PostgreSQL: booleanos reales, jsonb, uuid.
-- Idempotente y aditiva.

CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE OR REPLACE FUNCTION update_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE SCHEMA IF NOT EXISTS mail;

-- Rol de los motores. Sin contrasena aqui: se fija por operacion (ops/) y viaja
-- a Postfix/Dovecot por sus ficheros de mapa, nunca por el codigo.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'mail_engine') THEN
        CREATE ROLE mail_engine LOGIN;
    END IF;
END $$;

-- ── Dominios ──────────────────────────────────────────────────────────────────
-- active: si el dominio recibe y envia. backupmx: la celda actua como MX de
-- respaldo y reenvia al MX primario (relay_domains). relay_all_recipients y
-- relay_unknown_only gobiernan que destinatarios acepta en ese modo.
CREATE TABLE IF NOT EXISTS mail.domains (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id            uuid NOT NULL,
    domain               text NOT NULL UNIQUE,
    description          text NOT NULL DEFAULT '',
    active               boolean NOT NULL DEFAULT true,
    backupmx             boolean NOT NULL DEFAULT false,
    relay_all_recipients boolean NOT NULL DEFAULT false,
    relay_unknown_only   boolean NOT NULL DEFAULT false,
    relayhost_id         uuid,
    max_aliases          integer NOT NULL DEFAULT 0,
    max_mailboxes        integer NOT NULL DEFAULT 0,
    default_quota_bytes  bigint NOT NULL DEFAULT 0,
    max_quota_bytes      bigint NOT NULL DEFAULT 0,
    quota_bytes          bigint NOT NULL DEFAULT 0,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT domains_domain_lower CHECK (domain = lower(domain))
);
CREATE INDEX IF NOT EXISTS idx_mail_domains_tenant ON mail.domains (tenant_id);

-- Dominio alias: recibe como si fuera target_domain (user@alias -> user@target).
CREATE TABLE IF NOT EXISTS mail.alias_domains (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     uuid NOT NULL,
    alias_domain  text NOT NULL UNIQUE,
    target_domain text NOT NULL,
    active        boolean NOT NULL DEFAULT true,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT alias_domains_lower CHECK (alias_domain = lower(alias_domain) AND target_domain = lower(target_domain))
);
CREATE INDEX IF NOT EXISTS idx_mail_alias_domains_tenant ON mail.alias_domains (tenant_id);
CREATE INDEX IF NOT EXISTS idx_mail_alias_domains_target ON mail.alias_domains (target_domain);

-- ── Buzones ───────────────────────────────────────────────────────────────────
-- active: 0 = inactivo, 1 = activo, 2 = solo recibe (no puede iniciar sesion).
-- Es un tri-estado a proposito: un buzon dado de baja sigue recibiendo mientras
-- se decide que hacer con el correo, y Postfix y Dovecot lo distinguen.
-- mailbox_format y path_prefix componen la ruta que entregan Postfix y Dovecot.
-- kind: '' para personas; 'location', 'thing' o 'group' son recursos que no
-- reciben correo (Postfix los envia a null@localhost).
CREATE TABLE IF NOT EXISTS mail.mailboxes (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        uuid NOT NULL,
    username         text NOT NULL UNIQUE,
    local_part       text NOT NULL,
    domain           text NOT NULL,
    password_hash    text NOT NULL,
    display_name     text NOT NULL DEFAULT '',
    mailbox_format   text NOT NULL DEFAULT 'maildir:',
    path_prefix      text NOT NULL DEFAULT '/var/vmail/',
    quota_bytes      bigint NOT NULL DEFAULT 0,
    active           smallint NOT NULL DEFAULT 1 CHECK (active IN (0, 1, 2)),
    kind             text NOT NULL DEFAULT '' CHECK (kind IN ('', 'location', 'thing', 'group')),
    tls_enforce_in   boolean NOT NULL DEFAULT false,
    tls_enforce_out  boolean NOT NULL DEFAULT false,
    relayhost_id     uuid,
    imap_access      boolean NOT NULL DEFAULT true,
    pop3_access      boolean NOT NULL DEFAULT true,
    smtp_access      boolean NOT NULL DEFAULT true,
    sieve_access     boolean NOT NULL DEFAULT true,
    force_pw_update  boolean NOT NULL DEFAULT false,
    attributes       jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT mailboxes_username_lower CHECK (username = lower(username)),
    CONSTRAINT mailboxes_username_parts CHECK (username = local_part || '@' || domain)
);
CREATE INDEX IF NOT EXISTS idx_mail_mailboxes_tenant ON mail.mailboxes (tenant_id);
CREATE INDEX IF NOT EXISTS idx_mail_mailboxes_domain ON mail.mailboxes (domain);
CREATE INDEX IF NOT EXISTS idx_mail_mailboxes_kind ON mail.mailboxes (kind) WHERE kind <> '';

-- ── Aliases ───────────────────────────────────────────────────────────────────
-- address puede ser '@dominio' (catch-all). goto es la lista de destinos
-- separada por comas, como la entiende virtual_alias_maps. active tri-estado
-- como en buzones. sender_allowed: los buzones destino pueden enviar como el alias.
-- internal: solo se acepta desde la propia plataforma (Rspamd lo puntua alto).
CREATE TABLE IF NOT EXISTS mail.aliases (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       uuid NOT NULL,
    address         text NOT NULL UNIQUE,
    goto            text NOT NULL,
    domain          text NOT NULL,
    sender_allowed  boolean NOT NULL DEFAULT true,
    internal        boolean NOT NULL DEFAULT false,
    active          smallint NOT NULL DEFAULT 1 CHECK (active IN (0, 1, 2)),
    private_comment text NOT NULL DEFAULT '',
    public_comment  text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT aliases_address_lower CHECK (address = lower(address))
);
CREATE INDEX IF NOT EXISTS idx_mail_aliases_tenant ON mail.aliases (tenant_id);
CREATE INDEX IF NOT EXISTS idx_mail_aliases_domain ON mail.aliases (domain);

-- Aliases temporales (direcciones desechables) con caducidad.
CREATE TABLE IF NOT EXISTS mail.spam_aliases (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid NOT NULL,
    address     text NOT NULL UNIQUE,
    goto        text NOT NULL,
    description text NOT NULL DEFAULT '',
    valid_until timestamptz,
    permanent   boolean NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_mail_spam_aliases_tenant ON mail.spam_aliases (tenant_id);

-- Quien puede enviar como quien (smtpd_sender_login_maps). external: el
-- remitente es un dominio ajeno a la plataforma.
CREATE TABLE IF NOT EXISTS mail.sender_acl (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    uuid NOT NULL,
    logged_in_as text NOT NULL,
    send_as      text NOT NULL,
    external     boolean NOT NULL DEFAULT false,
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (logged_in_as, send_as)
);
CREATE INDEX IF NOT EXISTS idx_mail_sender_acl_tenant ON mail.sender_acl (tenant_id);

-- Contrasenas de aplicacion: una por cliente (movil, escritorio), revocable por
-- separado y acotada por protocolo. Las verifica el servicio de autenticacion de
-- motores, nunca Dovecot por SQL.
CREATE TABLE IF NOT EXISTS mail.app_passwords (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     uuid NOT NULL,
    mailbox_id    uuid NOT NULL,
    name          text NOT NULL,
    password_hash text NOT NULL,
    imap_access   boolean NOT NULL DEFAULT true,
    pop3_access   boolean NOT NULL DEFAULT true,
    smtp_access   boolean NOT NULL DEFAULT true,
    sieve_access  boolean NOT NULL DEFAULT true,
    dav_access    boolean NOT NULL DEFAULT true,
    active        boolean NOT NULL DEFAULT true,
    last_used_at  timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_mail_app_passwords_mailbox ON mail.app_passwords (mailbox_id);

-- ── Enrutado saliente ─────────────────────────────────────────────────────────
-- relayhosts: por donde sale el correo de un dominio o buzon (proveedor externo).
-- transports: destino -> siguiente salto para dominios concretos o por MX.
-- La contrasena SASL la lee Postfix en claro desde su mapa: no hay forma de que
-- un mapa pgsql descifre. Por eso estas dos tablas solo las ve mail_engine y el
-- servicio propietario, y la columna nunca sale por el API.
CREATE TABLE IF NOT EXISTS mail.relayhosts (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid NOT NULL,
    hostname   text NOT NULL,
    username   text NOT NULL DEFAULT '',
    password   text NOT NULL DEFAULT '',
    active     boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_mail_relayhosts_tenant ON mail.relayhosts (tenant_id);

CREATE TABLE IF NOT EXISTS mail.transports (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid,
    destination text NOT NULL,
    nexthop     text NOT NULL,
    username    text NOT NULL DEFAULT '',
    password    text NOT NULL DEFAULT '',
    is_mx_based boolean NOT NULL DEFAULT false,
    active      boolean NOT NULL DEFAULT true,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_mail_transports_destination ON mail.transports (destination);
CREATE INDEX IF NOT EXISTS idx_mail_transports_nexthop ON mail.transports (nexthop);

CREATE TABLE IF NOT EXISTS mail.tls_policy_overrides (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid NOT NULL,
    dest       text NOT NULL UNIQUE,
    policy     text NOT NULL CHECK (policy IN ('none', 'may', 'encrypt', 'dane', 'dane-only', 'fingerprint', 'verify', 'secure')),
    parameters text NOT NULL DEFAULT '',
    active     boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS mail.recipient_maps (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid NOT NULL,
    old_dest   text NOT NULL UNIQUE,
    new_dest   text NOT NULL,
    active     boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS mail.bcc_maps (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid NOT NULL,
    local_dest text NOT NULL,
    bcc_dest   text NOT NULL,
    domain     text NOT NULL,
    type       text NOT NULL CHECK (type IN ('sender', 'rcpt')),
    active     boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_mail_bcc_maps_local_dest ON mail.bcc_maps (local_dest);

-- ── Cuota y filtros: lo que ESCRIBE Dovecot ───────────────────────────────────
-- quota_usage la mantiene el dict de cuota de Dovecot; no tiene tenant_id porque
-- Dovecot no puede ponerlo. Se une a mailboxes por username.
CREATE TABLE IF NOT EXISTS mail.quota_usage (
    username text PRIMARY KEY,
    bytes    bigint NOT NULL DEFAULT 0,
    messages bigint NOT NULL DEFAULT 0
);

-- Filtros sieve por buzon. prefilter corre antes del script del usuario y
-- postfilter despues; Dovecot los lee por las vistas v_sieve_before/after.
CREATE TABLE IF NOT EXISTS mail.sieve_filters (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid NOT NULL,
    username    text NOT NULL,
    script_desc text NOT NULL DEFAULT '',
    script_name text NOT NULL DEFAULT 'active' CHECK (script_name IN ('active', 'inactive')),
    script_data text NOT NULL,
    filter_type text NOT NULL CHECK (filter_type IN ('prefilter', 'postfilter')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_mail_sieve_filters_username ON mail.sieve_filters (username);

CREATE OR REPLACE VIEW mail.v_sieve_before AS
    SELECT md5(script_data) AS id, username, script_name, script_data
      FROM mail.sieve_filters WHERE filter_type = 'prefilter';

CREATE OR REPLACE VIEW mail.v_sieve_after AS
    SELECT md5(script_data) AS id, username, script_name, script_data
      FROM mail.sieve_filters WHERE filter_type = 'postfilter';

-- ── Inicios de sesion en los motores (append-only) ────────────────────────────
CREATE TABLE IF NOT EXISTS mail.sasl_logins (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       uuid NOT NULL,
    username        text NOT NULL,
    service         text NOT NULL,
    app_password_id uuid,
    remote_ip       inet,
    logged_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_mail_sasl_logins_username_time ON mail.sasl_logins (username, logged_at DESC);

-- ── Triggers de updated_at ────────────────────────────────────────────────────
DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['domains','alias_domains','mailboxes','aliases','spam_aliases','app_passwords','relayhosts','transports','tls_policy_overrides','recipient_maps','bcc_maps','sieve_filters']
    LOOP
        IF NOT EXISTS (
            SELECT 1 FROM pg_trigger
             WHERE tgname = 'trg_mail_' || t || '_updated_at'
               AND tgrelid = ('mail.' || t)::regclass
        ) THEN
            EXECUTE format('CREATE TRIGGER trg_mail_%I_updated_at BEFORE UPDATE ON mail.%I FOR EACH ROW EXECUTE FUNCTION update_updated_at()', t, t);
        END IF;
    END LOOP;
END $$;

-- ── Permisos del rol de los motores ───────────────────────────────────────────
-- Solo lectura sobre lo que consultan Postfix y Dovecot; escritura unicamente
-- en el uso de cuota. Nada sobre app_passwords ni sasl_logins: la verificacion
-- de contrasenas la hace el servicio de autenticacion, no un mapa SQL.
GRANT USAGE ON SCHEMA mail TO mail_engine;
GRANT SELECT ON mail.domains, mail.alias_domains, mail.mailboxes, mail.aliases, mail.spam_aliases,
                mail.sender_acl, mail.relayhosts, mail.transports, mail.tls_policy_overrides,
                mail.recipient_maps, mail.bcc_maps, mail.sieve_filters,
                mail.v_sieve_before, mail.v_sieve_after TO mail_engine;
GRANT SELECT, INSERT, UPDATE, DELETE ON mail.quota_usage TO mail_engine;
