-- Schema: contacts | Service: contacts
--
-- Audiencia de marketing de la empresa: contactos con atributos declarados, el
-- consentimiento como evidencia (append-only), listas estaticas, segmentos dinamicos e
-- importaciones. campaigns lee la audiencia por POST /internal/contacts/audience; nadie
-- lee estas tablas fuera del servicio.
--
-- Idempotente y aditiva: se puede reejecutar sobre cualquier base de empresa.

CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE SCHEMA IF NOT EXISTS contacts;

CREATE OR REPLACE FUNCTION update_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- Atributos que la empresa declara para sus contactos. El valor de cada contacto se
-- valida contra esta tabla (tipo, obligatorio al crear); una clave no declarada se
-- rechaza. El tipo no se cambia una vez creado: los segmentos comparan por tipo.
CREATE TABLE IF NOT EXISTS contacts.attribute_definitions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    key varchar(63) NOT NULL,
    type varchar(10) NOT NULL,
    label varchar(200) NOT NULL DEFAULT '',
    required boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT contacts_attribute_definitions_tenant_key UNIQUE (tenant_id, key),
    CONSTRAINT contacts_attribute_definitions_key_check CHECK (key ~ '^[a-z][a-z0-9_]{0,62}$'),
    CONSTRAINT contacts_attribute_definitions_type_check CHECK (type IN ('string', 'number', 'boolean', 'date'))
);

DO $$
BEGIN
    CREATE TRIGGER trg_contacts_attribute_definitions_updated
        BEFORE UPDATE ON contacts.attribute_definitions
        FOR EACH ROW EXECUTE FUNCTION update_updated_at();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- marketing_consent es la proyeccion del consentimiento vigente de marketing (la ultima
-- fila de contacts.consents para el contacto). La mantiene el trigger de consents en la
-- misma transaccion que la evidencia; existe para que la audiencia filtre por indice y
-- no con una subconsulta por contacto. La fuente de verdad sigue siendo consents.
CREATE TABLE IF NOT EXISTS contacts.contacts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    email varchar(320) NOT NULL,
    first_name varchar(200) NOT NULL DEFAULT '',
    last_name varchar(200) NOT NULL DEFAULT '',
    locale varchar(16),
    timezone varchar(64),
    attributes jsonb NOT NULL DEFAULT '{}'::jsonb,
    tags text[] NOT NULL DEFAULT '{}',
    status varchar(20) NOT NULL DEFAULT 'active',
    marketing_consent varchar(10) NOT NULL DEFAULT 'none',
    source varchar(20) NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT contacts_contacts_tenant_email_key UNIQUE (tenant_id, email),
    CONSTRAINT contacts_contacts_email_lower CHECK (email = lower(email)),
    CONSTRAINT contacts_contacts_attributes_object CHECK (jsonb_typeof(attributes) = 'object'),
    CONSTRAINT contacts_contacts_status_check CHECK (status IN ('active', 'unsubscribed', 'bounced', 'complained')),
    CONSTRAINT contacts_contacts_consent_check CHECK (marketing_consent IN ('none', 'granted', 'revoked', 'pending')),
    CONSTRAINT contacts_contacts_source_check CHECK (source IN ('api', 'import', 'form', 'integration'))
);

CREATE INDEX IF NOT EXISTS idx_contacts_contacts_tenant_status
    ON contacts.contacts (tenant_id, status);
CREATE INDEX IF NOT EXISTS idx_contacts_contacts_tenant_created
    ON contacts.contacts (tenant_id, created_at);
CREATE INDEX IF NOT EXISTS idx_contacts_contacts_tags
    ON contacts.contacts USING gin (tags);
CREATE INDEX IF NOT EXISTS idx_contacts_contacts_attributes
    ON contacts.contacts USING gin (attributes jsonb_path_ops);
-- Recorrido de la audiencia: solo lo enviable, en orden de id (paginacion por keyset).
CREATE INDEX IF NOT EXISTS idx_contacts_contacts_sendable
    ON contacts.contacts (tenant_id, id)
    WHERE status = 'active' AND marketing_consent = 'granted';

DO $$
BEGIN
    CREATE TRIGGER trg_contacts_contacts_updated
        BEFORE UPDATE ON contacts.contacts
        FOR EACH ROW EXECUTE FUNCTION update_updated_at();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- Evidencia de consentimiento. APPEND-ONLY: el codigo solo inserta y el trigger rechaza
-- UPDATE, DELETE y TRUNCATE salvo en la transaccion de borrado del titular
-- (SET LOCAL app.erasure = 'on'), que seudonimiza: cambia contact_id por un id sin
-- vinculo, borra ip y user_agent y anota evidence.email_sha256. Sin clave foranea a
-- contacts a proposito: la fila sobrevive al contacto.
CREATE TABLE IF NOT EXISTS contacts.consents (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    contact_id uuid NOT NULL,
    purpose varchar(20) NOT NULL,
    status varchar(10) NOT NULL,
    method varchar(20) NOT NULL,
    source varchar(500) NOT NULL DEFAULT '',
    ip inet,
    user_agent varchar(512),
    evidence jsonb NOT NULL DEFAULT '{}'::jsonb,
    occurred_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT contacts_consents_purpose_check CHECK (purpose IN ('marketing')),
    CONSTRAINT contacts_consents_status_check CHECK (status IN ('granted', 'revoked', 'pending')),
    CONSTRAINT contacts_consents_method_check CHECK (
        method IN ('form', 'import', 'api', 'double_opt_in', 'unsubscribe_link', 'suppression')
    ),
    CONSTRAINT contacts_consents_evidence_object CHECK (jsonb_typeof(evidence) = 'object')
);

CREATE INDEX IF NOT EXISTS idx_contacts_consents_contact
    ON contacts.consents (tenant_id, contact_id, purpose, occurred_at DESC);

CREATE OR REPLACE FUNCTION contacts.consents_append_only()
RETURNS TRIGGER AS $$
BEGIN
    IF COALESCE(current_setting('app.erasure', true), '') <> 'on' THEN
        RAISE EXCEPTION 'contacts.consents es evidencia de consentimiento: % no permitido', TG_OP
            USING ERRCODE = 'insufficient_privilege';
    END IF;
    -- En el borrado solo se seudonimiza: la decision, su metodo, su origen y su fecha se
    -- conservan tal cual.
    IF TG_OP = 'UPDATE' AND (
        NEW.id IS DISTINCT FROM OLD.id OR
        NEW.tenant_id IS DISTINCT FROM OLD.tenant_id OR
        NEW.purpose IS DISTINCT FROM OLD.purpose OR
        NEW.status IS DISTINCT FROM OLD.status OR
        NEW.method IS DISTINCT FROM OLD.method OR
        NEW.source IS DISTINCT FROM OLD.source OR
        NEW.occurred_at IS DISTINCT FROM OLD.occurred_at
    ) THEN
        RAISE EXCEPTION 'el borrado del titular solo puede seudonimizar contacts.consents'
            USING ERRCODE = 'insufficient_privilege';
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    IF TG_OP = 'TRUNCATE' THEN
        RETURN NULL;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DO $$
BEGIN
    CREATE TRIGGER trg_contacts_consents_append_only
        BEFORE UPDATE OR DELETE ON contacts.consents
        FOR EACH ROW EXECUTE FUNCTION contacts.consents_append_only();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

DO $$
BEGIN
    CREATE TRIGGER trg_contacts_consents_no_truncate
        BEFORE TRUNCATE ON contacts.consents
        FOR EACH STATEMENT EXECUTE FUNCTION contacts.consents_append_only();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- Cada consentimiento nuevo de marketing pasa a ser el vigente del contacto. El servicio
-- bloquea la fila del contacto antes de insertar, asi que el orden de insercion es el de
-- occurred_at (clock_timestamp) y la proyeccion coincide con la ultima fila.
CREATE OR REPLACE FUNCTION contacts.consents_project_current()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.purpose = 'marketing' THEN
        UPDATE contacts.contacts
           SET marketing_consent = NEW.status
         WHERE id = NEW.contact_id AND tenant_id = NEW.tenant_id
           AND marketing_consent IS DISTINCT FROM NEW.status;
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

DO $$
BEGIN
    CREATE TRIGGER trg_contacts_consents_project
        AFTER INSERT ON contacts.consents
        FOR EACH ROW EXECUTE FUNCTION contacts.consents_project_current();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- Tokens del doble opt-in: solo se guarda el sha256 del token; el token viaja una vez,
-- en el enlace de confirmacion.
CREATE TABLE IF NOT EXISTS contacts.confirmation_tokens (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    contact_id uuid NOT NULL REFERENCES contacts.contacts (id) ON DELETE CASCADE,
    token_hash char(64) NOT NULL,
    expires_at timestamptz NOT NULL,
    used_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT contacts_confirmation_tokens_hash_key UNIQUE (token_hash),
    CONSTRAINT contacts_confirmation_tokens_hash_hex CHECK (token_hash ~ '^[0-9a-f]{64}$')
);

CREATE INDEX IF NOT EXISTS idx_contacts_confirmation_tokens_contact
    ON contacts.confirmation_tokens (tenant_id, contact_id);

CREATE TABLE IF NOT EXISTS contacts.lists (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    name varchar(200) NOT NULL,
    description text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT contacts_lists_tenant_name_key UNIQUE (tenant_id, name)
);

DO $$
BEGIN
    CREATE TRIGGER trg_contacts_lists_updated
        BEFORE UPDATE ON contacts.lists
        FOR EACH ROW EXECUTE FUNCTION update_updated_at();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

CREATE TABLE IF NOT EXISTS contacts.list_members (
    tenant_id uuid NOT NULL,
    list_id uuid NOT NULL REFERENCES contacts.lists (id) ON DELETE CASCADE,
    contact_id uuid NOT NULL REFERENCES contacts.contacts (id) ON DELETE CASCADE,
    added_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (list_id, contact_id)
);

CREATE INDEX IF NOT EXISTS idx_contacts_list_members_contact
    ON contacts.list_members (contact_id);

-- definition es el DSL de segmentos ya validado (internal/segment); se compila a SQL
-- parametrizado en cada evaluacion, nunca se guarda SQL.
CREATE TABLE IF NOT EXISTS contacts.segments (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    name varchar(200) NOT NULL,
    description text NOT NULL DEFAULT '',
    definition jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT contacts_segments_tenant_name_key UNIQUE (tenant_id, name),
    CONSTRAINT contacts_segments_definition_object CHECK (jsonb_typeof(definition) = 'object')
);

DO $$
BEGIN
    CREATE TRIGGER trg_contacts_segments_updated
        BEFORE UPDATE ON contacts.segments
        FOR EACH ROW EXECUTE FUNCTION update_updated_at();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- Rastro de cada importacion. errors guarda como maximo 100 filas {line, reason}; sin
-- la direccion, para no conservar datos de filas que no llegaron a ser contactos.
CREATE TABLE IF NOT EXISTS contacts.imports (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    status varchar(10) NOT NULL,
    total integer NOT NULL,
    created integer NOT NULL,
    updated integer NOT NULL,
    skipped integer NOT NULL,
    errors jsonb NOT NULL DEFAULT '[]'::jsonb,
    consent_basis text NOT NULL DEFAULT '',
    list_id uuid,
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT contacts_imports_status_check CHECK (status IN ('completed', 'failed')),
    CONSTRAINT contacts_imports_counts_check CHECK (
        total >= 0 AND created >= 0 AND updated >= 0 AND skipped >= 0
        AND created + updated + skipped <= total
    ),
    CONSTRAINT contacts_imports_errors_array CHECK (jsonb_typeof(errors) = 'array')
);

CREATE INDEX IF NOT EXISTS idx_contacts_imports_tenant_created
    ON contacts.imports (tenant_id, created_at DESC);
