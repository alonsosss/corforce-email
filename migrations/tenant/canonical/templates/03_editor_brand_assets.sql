-- Schema: templates | Service: templates
--
-- Editor visual de correos (docs/adr/0012, docs/Plan_Editor_Correos.md, seccion 3):
--
-- * versions.editor: el documento del editor junto a la version ({kind, project, mjml}), solo para
--   volver a editarla; nunca se envia ni se interpreta. NULL = HTML escrito a mano. El servicio
--   limita el objeto compacto a 2 MiB; aqui el tope es mas holgado porque el texto de un jsonb lleva
--   espacios que la forma compacta no.
-- * El contenido de una version que ya estuvo publicada (asunto, HTML, texto, variables y editor) no
--   cambia: lo garantiza un trigger ademas del caso de uso, que no tiene ninguna ruta de edicion.
-- * brand_kits: kit de marca de la empresa (logo, colores, tipografias y pie legal). Una fila por
--   empresa. La direccion del pie es la que exige el verificador para publicar marketing.
-- * assets: imagenes subidas para las plantillas. La clave del objeto lleva el sha256 del contenido,
--   asi que la misma imagen no se guarda dos veces por empresa. El borrado es logico: los correos ya
--   enviados siguen mostrando la imagen.
--
-- Idempotente y aditiva: una columna nueva nula, tablas nuevas y restricciones que ninguna fila
-- existente incumple. Las tablas nuevas quedan cubiertas por el ALTER DEFAULT PRIVILEGES de
-- 02_service_role.sql.

ALTER TABLE templates.versions ADD COLUMN IF NOT EXISTS editor jsonb;

ALTER TABLE templates.versions DROP CONSTRAINT IF EXISTS versions_editor_check;
ALTER TABLE templates.versions ADD CONSTRAINT versions_editor_check
    CHECK (editor IS NULL OR (jsonb_typeof(editor) = 'object' AND octet_length(editor::text) <= 3145728));

CREATE OR REPLACE FUNCTION templates.versions_content_immutable()
RETURNS TRIGGER AS $$
BEGIN
    IF OLD.status <> 'draft' AND (
        NEW.subject IS DISTINCT FROM OLD.subject OR
        NEW.html IS DISTINCT FROM OLD.html OR
        NEW.text IS DISTINCT FROM OLD.text OR
        NEW.variables IS DISTINCT FROM OLD.variables OR
        NEW.editor IS DISTINCT FROM OLD.editor) THEN
        RAISE EXCEPTION 'el contenido de una version publicada no cambia (version %)', OLD.id
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DO $$
BEGIN
    CREATE TRIGGER trg_versions_content_immutable
        BEFORE UPDATE ON templates.versions
        FOR EACH ROW EXECUTE FUNCTION templates.versions_content_immutable();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- colors: #RRGGBB en mayusculas, hasta 12. fonts: nombres de la lista cerrada del servicio, hasta 6.
-- logo_asset_id apunta a templates.assets de la misma empresa; lo comprueba el servicio y la fila
-- se conserva aunque la imagen se retire del listado.
CREATE TABLE IF NOT EXISTS templates.brand_kits (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    logo_asset_id uuid,
    colors text[] NOT NULL DEFAULT '{}',
    fonts text[] NOT NULL DEFAULT '{}',
    footer_company varchar(200) NOT NULL DEFAULT '',
    footer_address varchar(500) NOT NULL DEFAULT '',
    footer_website varchar(2048) NOT NULL DEFAULT '',
    footer_support_email varchar(320) NOT NULL DEFAULT '',
    updated_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT brand_kits_tenant_key UNIQUE (tenant_id),
    CONSTRAINT brand_kits_colors_check CHECK (cardinality(colors) <= 12),
    CONSTRAINT brand_kits_fonts_check CHECK (cardinality(fonts) <= 6)
);

DO $$
BEGIN
    CREATE TRIGGER trg_brand_kits_updated
        BEFORE UPDATE ON templates.brand_kits
        FOR EACH ROW EXECUTE FUNCTION templates.update_updated_at();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- object_key: public/<tenant>/templates/<sha256>.<ext>, la clave del almacen de objetos que sirve el
-- gateway. Los topes (5 MiB, 4000x4000) se repiten aqui para que ninguna escritura fuera del API
-- los salte.
CREATE TABLE IF NOT EXISTS templates.assets (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    sha256 char(64) NOT NULL,
    object_key text NOT NULL,
    content_type varchar(20) NOT NULL,
    size_bytes integer NOT NULL,
    width integer NOT NULL,
    height integer NOT NULL,
    name varchar(255) NOT NULL,
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz,
    CONSTRAINT assets_tenant_sha256_key UNIQUE (tenant_id, sha256),
    CONSTRAINT assets_sha256_check CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    CONSTRAINT assets_content_type_check CHECK (content_type IN ('image/png', 'image/jpeg', 'image/gif', 'image/webp')),
    CONSTRAINT assets_size_check CHECK (size_bytes BETWEEN 1 AND 5242880),
    CONSTRAINT assets_dimensions_check CHECK (width BETWEEN 1 AND 4000 AND height BETWEEN 1 AND 4000)
);

CREATE INDEX IF NOT EXISTS idx_assets_tenant_listing
    ON templates.assets (tenant_id, created_at DESC, id DESC) WHERE deleted_at IS NULL;

DO $$
BEGIN
    CREATE TRIGGER trg_assets_updated
        BEFORE UPDATE ON templates.assets
        FOR EACH ROW EXECUTE FUNCTION templates.update_updated_at();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;
