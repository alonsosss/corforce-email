-- Schema: templates | Service: templates
--
-- Plantillas de correo versionadas por empresa. La plantilla es la cabecera estable
-- (nombre, tipo, estado) y cada version guarda el contenido inmutable (asunto, HTML,
-- texto y variables declaradas). Solo una version esta publicada a la vez; las que
-- estuvieron publicadas quedan como supersedidas para poder volver a ellas y para que un
-- envio que fijo una version concreta siga renderizando con ella.
--
-- Idempotente y aditiva: se puede reejecutar sobre cualquier base de empresa.

CREATE SCHEMA IF NOT EXISTS templates;

-- Trigger de updated_at propio del esquema: no depende de que otra migracion haya
-- definido la funcion publica ni del orden en que se apliquen.
CREATE OR REPLACE FUNCTION templates.update_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- current_version = 0 significa que ninguna version esta publicada todavia.
CREATE TABLE IF NOT EXISTS templates.templates (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    name varchar(120) NOT NULL,
    description text NOT NULL DEFAULT '',
    kind varchar(20) NOT NULL,
    status varchar(20) NOT NULL DEFAULT 'active',
    current_version integer NOT NULL DEFAULT 0,
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT templates_tenant_name_key UNIQUE (tenant_id, name),
    CONSTRAINT templates_kind_check CHECK (kind IN ('transactional', 'marketing')),
    CONSTRAINT templates_status_check CHECK (status IN ('active', 'archived')),
    CONSTRAINT templates_current_version_check CHECK (current_version >= 0)
);

CREATE INDEX IF NOT EXISTS idx_templates_tenant_status_kind
    ON templates.templates (tenant_id, status, kind);

DO $$
BEGIN
    CREATE TRIGGER trg_templates_updated
        BEFORE UPDATE ON templates.templates
        FOR EACH ROW EXECUTE FUNCTION templates.update_updated_at();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- text NULL: la parte de texto se genera a partir del HTML al renderizar.
-- variables: lista [{name, type, required, default?}] con type en
-- string|number|boolean|url|email; la valida el servicio antes de guardar.
-- Los limites de tamano (asunto 998 bytes, HTML 512 KiB) se repiten aqui para que
-- ninguna escritura fuera del API los salte.
CREATE TABLE IF NOT EXISTS templates.versions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    template_id uuid NOT NULL REFERENCES templates.templates(id) ON DELETE CASCADE,
    version integer NOT NULL,
    subject text NOT NULL,
    html text NOT NULL,
    text text,
    variables jsonb NOT NULL DEFAULT '[]'::jsonb,
    status varchar(20) NOT NULL DEFAULT 'draft',
    published_at timestamptz,
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT versions_template_version_key UNIQUE (template_id, version),
    CONSTRAINT versions_version_check CHECK (version >= 1),
    CONSTRAINT versions_status_check CHECK (status IN ('draft', 'published', 'superseded')),
    CONSTRAINT versions_subject_size_check CHECK (octet_length(subject) <= 998),
    CONSTRAINT versions_html_size_check CHECK (octet_length(html) <= 524288),
    CONSTRAINT versions_variables_is_array CHECK (jsonb_typeof(variables) = 'array'),
    -- Una version publicada o supersedida tiene fecha de publicacion; un borrador, no.
    CONSTRAINT versions_published_at_check CHECK ((status = 'draft') = (published_at IS NULL))
);

-- Solo una version publicada por plantilla, garantizado por la base y no solo por el
-- caso de uso.
CREATE UNIQUE INDEX IF NOT EXISTS uq_versions_one_published
    ON templates.versions (template_id) WHERE status = 'published';

CREATE INDEX IF NOT EXISTS idx_versions_tenant_template
    ON templates.versions (tenant_id, template_id, version DESC);
