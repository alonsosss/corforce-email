-- Schema: mail_dav | Service: mail-dav
--
-- Planificacion sobre los calendarios personales (docs/adr/0004, "Invitaciones, disponibilidad y citas";
-- docs/Plan_Webmail_Innovador.md, bloque C3):
--
-- * event_busy: la ocupacion de cada evento materializada al guardarlo (inicio y fin de sus apariciones que
--   ocupan tiempo, sin titulo ni nada mas), hasta events.busy_until (NULL: todas). La disponibilidad del equipo
--   se sirve de aqui y nunca lee un evento de otro buzon. Las filas de eventos anteriores a esta migracion
--   quedan con busy_until = -infinity, que las marca como pendientes: el servicio las materializa la primera vez
--   que alguien consulta la disponibilidad de ese buzon.
-- * mailbox_addresses: la direccion de cada buzon que usa el calendario, para pedir la disponibilidad de un
--   companero por su direccion (el directorio de la empresa esta en la celda y no se lee desde aqui).
-- * booking_pages y bookings: la pagina publica de citas de un buzon (una por buzon) y el registro minimo de sus
--   reservas para los topes diarios (sin el nombre ni la nota del visitante, que van en el evento del dueno, y
--   con su correo solo como SHA-256).
--
-- Las politicas de fila de 02_service_role.sql dejan a cada sesion solo su buzon; lo que cruza buzones de la
-- MISMA empresa lo hacen funciones SECURITY DEFINER acotadas (search_path fijo, empresa de la sesion, ventana y
-- numero de filas con tope, solo EXECUTE para mail_dav_service): la ocupacion devuelve inicio y fin y nada mas.
--
-- Idempotente y aditiva.

ALTER TABLE mail_dav.events ADD COLUMN IF NOT EXISTS busy_until timestamptz DEFAULT '-infinity';

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'mail_dav_events_owner_unique') THEN
        ALTER TABLE mail_dav.events ADD CONSTRAINT mail_dav_events_owner_unique UNIQUE (id, tenant_id, mailbox_id);
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_mail_dav_events_busy_pending ON mail_dav.events (tenant_id, mailbox_id, busy_until)
    WHERE busy_until IS NOT NULL;

CREATE TABLE IF NOT EXISTS mail_dav.event_busy (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    event_id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    mailbox_id uuid NOT NULL,
    starts_at timestamptz NOT NULL,
    ends_at timestamptz NOT NULL,
    CONSTRAINT mail_dav_event_busy_event_fk FOREIGN KEY (event_id, tenant_id, mailbox_id)
        REFERENCES mail_dav.events (id, tenant_id, mailbox_id) ON DELETE CASCADE,
    CONSTRAINT mail_dav_event_busy_range_check CHECK (ends_at >= starts_at)
);

CREATE INDEX IF NOT EXISTS idx_mail_dav_event_busy_mailbox ON mail_dav.event_busy (tenant_id, mailbox_id, starts_at);
CREATE INDEX IF NOT EXISTS idx_mail_dav_event_busy_event ON mail_dav.event_busy (event_id);

CREATE TABLE IF NOT EXISTS mail_dav.mailbox_addresses (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    mailbox_id uuid NOT NULL,
    address varchar(320) NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT mail_dav_mailbox_addresses_mailbox_unique UNIQUE (tenant_id, mailbox_id),
    CONSTRAINT mail_dav_mailbox_addresses_address_unique UNIQUE (tenant_id, address),
    CONSTRAINT mail_dav_mailbox_addresses_address_check CHECK (address = lower(address) AND address ~ '^[^[:space:]@]+@[^[:space:]@]+$')
);

DO $$
BEGIN
    CREATE TRIGGER trg_mail_dav_mailbox_addresses_updated
        BEFORE UPDATE ON mail_dav.mailbox_addresses
        FOR EACH ROW EXECUTE FUNCTION update_updated_at();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

CREATE TABLE IF NOT EXISTS mail_dav.booking_pages (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    mailbox_id uuid NOT NULL,
    public_id varchar(64) NOT NULL,
    title varchar(200) NOT NULL,
    description varchar(2000) NOT NULL DEFAULT '',
    duration_minutes integer NOT NULL,
    buffer_minutes integer NOT NULL DEFAULT 0,
    min_notice_minutes integer NOT NULL DEFAULT 0,
    max_advance_days integer NOT NULL,
    daily_limit integer NOT NULL,
    timezone varchar(64) NOT NULL,
    weekly jsonb NOT NULL,
    active boolean NOT NULL DEFAULT false,
    owner_address varchar(320) NOT NULL,
    owner_name varchar(200) NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT mail_dav_booking_pages_mailbox_unique UNIQUE (tenant_id, mailbox_id),
    CONSTRAINT mail_dav_booking_pages_public_unique UNIQUE (public_id),
    CONSTRAINT mail_dav_booking_pages_owner_unique UNIQUE (id, tenant_id, mailbox_id),
    CONSTRAINT mail_dav_booking_pages_public_check CHECK (public_id ~ '^[A-Za-z0-9_-]{22,64}$'),
    CONSTRAINT mail_dav_booking_pages_ranges_check CHECK (
        duration_minutes BETWEEN 5 AND 480 AND buffer_minutes BETWEEN 0 AND 240
        AND min_notice_minutes BETWEEN 0 AND 43200 AND max_advance_days BETWEEN 1 AND 365 AND daily_limit >= 1),
    CONSTRAINT mail_dav_booking_pages_weekly_check CHECK (jsonb_typeof(weekly) = 'object' AND octet_length(weekly::text) <= 8192)
);

DO $$
BEGIN
    CREATE TRIGGER trg_mail_dav_booking_pages_updated
        BEFORE UPDATE ON mail_dav.booking_pages
        FOR EACH ROW EXECUTE FUNCTION update_updated_at();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

CREATE TABLE IF NOT EXISTS mail_dav.bookings (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    mailbox_id uuid NOT NULL,
    page_id uuid NOT NULL,
    event_resource varchar(200) NOT NULL,
    starts_at timestamptz NOT NULL,
    ends_at timestamptz NOT NULL,
    visitor_hash char(64) NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT mail_dav_bookings_page_fk FOREIGN KEY (page_id, tenant_id, mailbox_id)
        REFERENCES mail_dav.booking_pages (id, tenant_id, mailbox_id) ON DELETE CASCADE,
    CONSTRAINT mail_dav_bookings_range_check CHECK (ends_at > starts_at),
    CONSTRAINT mail_dav_bookings_hash_check CHECK (visitor_hash ~ '^[0-9a-f]{64}$')
);

CREATE INDEX IF NOT EXISTS idx_mail_dav_bookings_page ON mail_dav.bookings (page_id, created_at);

GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA mail_dav TO mail_dav_service;

DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['event_busy', 'mailbox_addresses', 'booking_pages', 'bookings']
    LOOP
        EXECUTE format('ALTER TABLE mail_dav.%I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('DROP POLICY IF EXISTS mailbox_isolation ON mail_dav.%I', t);
        EXECUTE format(
            'CREATE POLICY mailbox_isolation ON mail_dav.%I FOR ALL TO mail_dav_service '
            'USING (tenant_id = mail_dav.current_tenant() AND mailbox_id = mail_dav.current_mailbox()) '
            'WITH CHECK (tenant_id = mail_dav.current_tenant() AND mailbox_id = mail_dav.current_mailbox())', t);
    END LOOP;
END $$;

-- La direccion de un buzon la registra el propio buzon (empresa y buzon de la sesion). Un buzon borrado y creado
-- de nuevo con el mismo nombre tiene otro id: su direccion pasa al nuevo, y eso exige tocar la fila del anterior,
-- que la politica de fila no deja ver.
CREATE OR REPLACE FUNCTION mail_dav.register_mailbox_address(p_tenant uuid, p_mailbox uuid, p_address text)
RETURNS void
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog, mail_dav
AS $$
BEGIN
    IF p_tenant IS NULL OR p_mailbox IS NULL
       OR p_tenant IS DISTINCT FROM mail_dav.current_tenant() OR p_mailbox IS DISTINCT FROM mail_dav.current_mailbox() THEN
        RAISE EXCEPTION 'el buzon no es el de la sesion' USING ERRCODE = '42501';
    END IF;
    IF p_address IS NULL OR p_address <> lower(p_address) OR length(p_address) > 320 OR p_address !~ '^[^[:space:]@]+@[^[:space:]@]+$' THEN
        RAISE EXCEPTION 'direccion no valida' USING ERRCODE = '22023';
    END IF;
    DELETE FROM mail_dav.mailbox_addresses WHERE tenant_id = p_tenant AND address = p_address AND mailbox_id <> p_mailbox;
    INSERT INTO mail_dav.mailbox_addresses (tenant_id, mailbox_id, address) VALUES (p_tenant, p_mailbox, p_address)
    ON CONFLICT (tenant_id, mailbox_id) DO UPDATE SET address = EXCLUDED.address
     WHERE mail_dav.mailbox_addresses.address <> EXCLUDED.address;
END
$$;

-- Buzones de la empresa de la sesion con esas direcciones (como mucho 50).
CREATE OR REPLACE FUNCTION mail_dav.resolve_busy_mailboxes(p_tenant uuid, p_addresses text[])
RETURNS TABLE (address text, mailbox_id uuid)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, mail_dav
AS $$
BEGIN
    IF p_tenant IS NULL OR p_tenant IS DISTINCT FROM mail_dav.current_tenant() THEN
        RAISE EXCEPTION 'la empresa no es la de la sesion' USING ERRCODE = '42501';
    END IF;
    IF coalesce(cardinality(p_addresses), 0) > 50 THEN
        RAISE EXCEPTION 'demasiadas direcciones' USING ERRCODE = '22023';
    END IF;
    RETURN QUERY
        SELECT a.address::text, a.mailbox_id FROM mail_dav.mailbox_addresses a
         WHERE a.tenant_id = p_tenant AND a.address = ANY (p_addresses)
         ORDER BY a.address;
END
$$;

-- La ocupacion de buzones de la empresa de la sesion en una ventana de como mucho 62 dias: inicio y fin, nada mas.
CREATE OR REPLACE FUNCTION mail_dav.busy_intervals(p_tenant uuid, p_mailboxes uuid[], p_from timestamptz, p_to timestamptz)
RETURNS TABLE (mailbox_id uuid, starts_at timestamptz, ends_at timestamptz)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, mail_dav
AS $$
BEGIN
    IF p_tenant IS NULL OR p_tenant IS DISTINCT FROM mail_dav.current_tenant() THEN
        RAISE EXCEPTION 'la empresa no es la de la sesion' USING ERRCODE = '42501';
    END IF;
    IF p_from IS NULL OR p_to IS NULL OR p_to <= p_from OR p_to - p_from > interval '62 days' THEN
        RAISE EXCEPTION 'ventana no valida' USING ERRCODE = '22023';
    END IF;
    IF coalesce(cardinality(p_mailboxes), 0) > 50 THEN
        RAISE EXCEPTION 'demasiados buzones' USING ERRCODE = '22023';
    END IF;
    RETURN QUERY
        SELECT b.mailbox_id, b.starts_at, b.ends_at FROM mail_dav.event_busy b
         WHERE b.tenant_id = p_tenant AND b.mailbox_id = ANY (p_mailboxes)
           AND b.starts_at < p_to AND b.ends_at > p_from
         ORDER BY b.mailbox_id, b.starts_at
         LIMIT 20000;
END
$$;

-- Eventos de esos buzones cuya ocupacion no esta materializada hasta p_until: sus ids y nada mas, para que el
-- servicio los complete con la identidad de su propio buzon.
CREATE OR REPLACE FUNCTION mail_dav.busy_pending_events(p_tenant uuid, p_mailboxes uuid[], p_until timestamptz, p_limit integer)
RETURNS TABLE (mailbox_id uuid, event_id uuid)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, mail_dav
AS $$
BEGIN
    IF p_tenant IS NULL OR p_tenant IS DISTINCT FROM mail_dav.current_tenant() THEN
        RAISE EXCEPTION 'la empresa no es la de la sesion' USING ERRCODE = '42501';
    END IF;
    IF coalesce(cardinality(p_mailboxes), 0) > 50 THEN
        RAISE EXCEPTION 'demasiados buzones' USING ERRCODE = '22023';
    END IF;
    RETURN QUERY
        SELECT e.mailbox_id, e.id FROM mail_dav.events e
         WHERE e.tenant_id = p_tenant AND e.mailbox_id = ANY (p_mailboxes)
           AND e.busy_until IS NOT NULL AND e.busy_until < p_until
         ORDER BY e.mailbox_id, e.id
         LIMIT least(greatest(p_limit, 1), 1000);
END
$$;

-- El buzon dueno de una pagina de citas ACTIVA de la empresa de la sesion, por el identificador de su enlace. La
-- pagina publica llega sin buzon: con esto el servicio sabe en nombre de quien trabajar.
CREATE OR REPLACE FUNCTION mail_dav.booking_page_owner(p_tenant uuid, p_public_id text)
RETURNS uuid
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, mail_dav
AS $$
DECLARE owner uuid;
BEGIN
    IF p_tenant IS NULL OR p_tenant IS DISTINCT FROM mail_dav.current_tenant() THEN
        RAISE EXCEPTION 'la empresa no es la de la sesion' USING ERRCODE = '42501';
    END IF;
    SELECT p.mailbox_id INTO owner FROM mail_dav.booking_pages p
     WHERE p.tenant_id = p_tenant AND p.public_id = p_public_id AND p.active;
    RETURN owner;
END
$$;

DO $$
DECLARE f text;
BEGIN
    FOREACH f IN ARRAY ARRAY[
        'mail_dav.register_mailbox_address(uuid, uuid, text)',
        'mail_dav.resolve_busy_mailboxes(uuid, text[])',
        'mail_dav.busy_intervals(uuid, uuid[], timestamptz, timestamptz)',
        'mail_dav.busy_pending_events(uuid, uuid[], timestamptz, integer)',
        'mail_dav.booking_page_owner(uuid, text)']
    LOOP
        EXECUTE format('REVOKE ALL ON FUNCTION %s FROM PUBLIC', f);
        IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'mail_dav_service') THEN
            EXECUTE format('GRANT EXECUTE ON FUNCTION %s TO mail_dav_service', f);
        END IF;
    END LOOP;
END $$;
