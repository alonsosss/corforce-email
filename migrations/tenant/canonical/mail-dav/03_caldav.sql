-- Schema: mail_dav | Service: mail-dav
--
-- Calendarios y eventos personales de cada buzon (CalDAV, docs/adr/0004). Mismo modelo que las libretas y
-- los contactos de 01_mail_dav.sql: los eventos son datos de la empresa y viven en su base; el buzon dueno
-- vive en la celda (mail-directory), asi que aqui solo se guarda su id, sin clave foranea entre esquemas.
--
-- Un iCalendar se guarda tal cual lo envio el cliente (ical), con su etag (SHA-256 de esos bytes) y los
-- campos indexados que hacen falta para listarlo y para descartar por tiempo en un calendar-query:
-- first_start es el inicio de la primera aparicion y last_end el fin de la ultima; NULL en last_end es una
-- recurrencia sin fin (o que el servidor no sabe acotar), que no se descarta por tiempo. Son un descarte
-- previo: el servicio decide con exactitud sobre lo que devuelve. La recurrencia no se expande al guardar
-- ni se guarda expandida.
--
-- calendar_changes es el registro de cambios de cada calendario para sync-collection (RFC 6578), con el
-- mismo modelo que collection_changes: no se reutiliza esa tabla porque su clave foranea compuesta apunta a
-- addressbooks y es la que impide que un cambio cuelgue de la coleccion de otro buzon.
--
-- Aislamiento: igual que 02_service_role.sql. Se repiten aqui los permisos y las politicas de las tablas
-- nuevas para que esta migracion sea completa por si sola.
--
-- Idempotente y aditiva: se puede reejecutar sobre cualquier base de empresa.

CREATE TABLE IF NOT EXISTS mail_dav.calendars (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    mailbox_id uuid NOT NULL,
    slug varchar(63) NOT NULL,
    display_name varchar(200) NOT NULL,
    description varchar(1000) NOT NULL DEFAULT '',
    sync_seq bigint NOT NULL DEFAULT 0,
    changes_floor bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT mail_dav_calendars_slug_check CHECK (slug ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
    CONSTRAINT mail_dav_calendars_seq_check CHECK (sync_seq >= 0 AND changes_floor >= 0 AND changes_floor <= sync_seq),
    CONSTRAINT mail_dav_calendars_slug_unique UNIQUE (tenant_id, mailbox_id, slug),
    CONSTRAINT mail_dav_calendars_owner_unique UNIQUE (id, tenant_id, mailbox_id)
);

DO $$
BEGIN
    CREATE TRIGGER trg_mail_dav_calendars_updated
        BEFORE UPDATE ON mail_dav.calendars
        FOR EACH ROW EXECUTE FUNCTION update_updated_at();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- La clave foranea lleva empresa y buzon: un evento no puede apuntar al calendario de otro buzon.
CREATE TABLE IF NOT EXISTS mail_dav.events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    mailbox_id uuid NOT NULL,
    calendar_id uuid NOT NULL,
    resource_name varchar(200) NOT NULL,
    uid varchar(255) NOT NULL,
    ical text NOT NULL,
    etag char(64) NOT NULL,
    summary varchar(300) NOT NULL DEFAULT '',
    first_start timestamptz NOT NULL,
    last_end timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT mail_dav_events_calendar_fk FOREIGN KEY (calendar_id, tenant_id, mailbox_id)
        REFERENCES mail_dav.calendars (id, tenant_id, mailbox_id) ON DELETE CASCADE,
    CONSTRAINT mail_dav_events_resource_check CHECK (resource_name ~ '^[A-Za-z0-9][A-Za-z0-9._@=+~-]{0,195}\.ics$'),
    CONSTRAINT mail_dav_events_etag_check CHECK (etag ~ '^[0-9a-f]{64}$'),
    CONSTRAINT mail_dav_events_size_check CHECK (octet_length(ical) <= 4194304),
    CONSTRAINT mail_dav_events_range_check CHECK (last_end IS NULL OR last_end >= first_start),
    CONSTRAINT mail_dav_events_resource_unique UNIQUE (calendar_id, resource_name),
    CONSTRAINT mail_dav_events_uid_unique UNIQUE (calendar_id, uid)
);

DO $$
BEGIN
    CREATE TRIGGER trg_mail_dav_events_updated
        BEFORE UPDATE ON mail_dav.events
        FOR EACH ROW EXECUTE FUNCTION update_updated_at();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- El limite de eventos por buzon cuenta por aqui, y el descarte por tiempo de calendar-query por el otro.
CREATE INDEX IF NOT EXISTS idx_mail_dav_events_mailbox ON mail_dav.events (tenant_id, mailbox_id);
CREATE INDEX IF NOT EXISTS idx_mail_dav_events_range ON mail_dav.events (calendar_id, first_start);

CREATE TABLE IF NOT EXISTS mail_dav.calendar_changes (
    calendar_id uuid NOT NULL,
    seq bigint NOT NULL,
    tenant_id uuid NOT NULL,
    mailbox_id uuid NOT NULL,
    resource_name varchar(200) NOT NULL,
    deleted boolean NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (calendar_id, seq),
    CONSTRAINT mail_dav_calendar_changes_calendar_fk FOREIGN KEY (calendar_id, tenant_id, mailbox_id)
        REFERENCES mail_dav.calendars (id, tenant_id, mailbox_id) ON DELETE CASCADE,
    CONSTRAINT mail_dav_calendar_changes_seq_check CHECK (seq > 0)
);

CREATE INDEX IF NOT EXISTS idx_mail_dav_calendar_changes_resource ON mail_dav.calendar_changes (calendar_id, resource_name, seq DESC);

GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA mail_dav TO mail_dav_service;

DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['calendars', 'events', 'calendar_changes']
    LOOP
        EXECUTE format('ALTER TABLE mail_dav.%I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('DROP POLICY IF EXISTS mailbox_isolation ON mail_dav.%I', t);
        EXECUTE format(
            'CREATE POLICY mailbox_isolation ON mail_dav.%I FOR ALL TO mail_dav_service '
            'USING (tenant_id = mail_dav.current_tenant() AND mailbox_id = mail_dav.current_mailbox()) '
            'WITH CHECK (tenant_id = mail_dav.current_tenant() AND mailbox_id = mail_dav.current_mailbox())', t);
    END LOOP;
END $$;
