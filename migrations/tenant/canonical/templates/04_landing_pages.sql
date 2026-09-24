-- Schema: templates | Service: templates
--
-- Paginas de aterrizaje (docs/Plan_Marketing_Avanzado.md, 2-F): un documento del editor en modo
-- web, versionado como las plantillas y publicado en <PUBLIC_BASE_URL>/p/<empresa>/<slug>.
--
-- * pages: la cabecera estable. slug es la direccion publica dentro de la empresa; current_version
--   = 0 mientras no haya version publicada. noindex pide a los buscadores que no la indexen.
-- * page_versions: el contenido. html y css son los que se sirven, ya saneados por el servicio al
--   guardar (sin scripts, formularios ni marcos del usuario; el formulario de suscripcion va como
--   marcador que el servicio sustituye al servir). editor es el documento de GrapesJS para volver a
--   editarla. Una version que estuvo publicada no cambia (trigger).
--
-- Idempotente y aditiva: tablas nuevas cubiertas por el ALTER DEFAULT PRIVILEGES de
-- 02_service_role.sql.

CREATE TABLE IF NOT EXISTS templates.pages (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    name varchar(120) NOT NULL,
    slug varchar(80) NOT NULL,
    status varchar(20) NOT NULL DEFAULT 'active',
    noindex boolean NOT NULL DEFAULT true,
    current_version integer NOT NULL DEFAULT 0,
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT pages_tenant_name_key UNIQUE (tenant_id, name),
    CONSTRAINT pages_tenant_slug_key UNIQUE (tenant_id, slug),
    CONSTRAINT pages_slug_check CHECK (slug ~ '^[a-z0-9]([a-z0-9-]{0,78}[a-z0-9])?$'),
    CONSTRAINT pages_status_check CHECK (status IN ('active', 'archived')),
    CONSTRAINT pages_current_version_check CHECK (current_version >= 0)
);

DO $$
BEGIN
    CREATE TRIGGER trg_pages_updated
        BEFORE UPDATE ON templates.pages
        FOR EACH ROW EXECUTE FUNCTION templates.update_updated_at();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

CREATE TABLE IF NOT EXISTS templates.page_versions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    page_id uuid NOT NULL REFERENCES templates.pages (id) ON DELETE CASCADE,
    version integer NOT NULL,
    title varchar(200) NOT NULL,
    description varchar(500) NOT NULL DEFAULT '',
    html text NOT NULL,
    css text NOT NULL DEFAULT '',
    editor jsonb,
    status varchar(20) NOT NULL DEFAULT 'draft',
    published_at timestamptz,
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT page_versions_page_version_key UNIQUE (page_id, version),
    CONSTRAINT page_versions_version_check CHECK (version >= 1),
    CONSTRAINT page_versions_status_check CHECK (status IN ('draft', 'published', 'superseded')),
    CONSTRAINT page_versions_html_size_check CHECK (octet_length(html) <= 524288),
    CONSTRAINT page_versions_css_size_check CHECK (octet_length(css) <= 262144),
    CONSTRAINT page_versions_editor_check
        CHECK (editor IS NULL OR (jsonb_typeof(editor) = 'object' AND octet_length(editor::text) <= 3145728)),
    CONSTRAINT page_versions_published_at_check CHECK ((status = 'draft') = (published_at IS NULL))
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_page_versions_one_published
    ON templates.page_versions (page_id) WHERE status = 'published';

CREATE INDEX IF NOT EXISTS idx_page_versions_tenant_page
    ON templates.page_versions (tenant_id, page_id, version DESC);

CREATE OR REPLACE FUNCTION templates.page_versions_content_immutable()
RETURNS TRIGGER AS $$
BEGIN
    IF OLD.status <> 'draft' AND (
        NEW.title IS DISTINCT FROM OLD.title OR
        NEW.description IS DISTINCT FROM OLD.description OR
        NEW.html IS DISTINCT FROM OLD.html OR
        NEW.css IS DISTINCT FROM OLD.css OR
        NEW.editor IS DISTINCT FROM OLD.editor) THEN
        RAISE EXCEPTION 'el contenido de una version publicada no cambia (version %)', OLD.id
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DO $$
BEGIN
    CREATE TRIGGER trg_page_versions_content_immutable
        BEFORE UPDATE ON templates.page_versions
        FOR EACH ROW EXECUTE FUNCTION templates.page_versions_content_immutable();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;
