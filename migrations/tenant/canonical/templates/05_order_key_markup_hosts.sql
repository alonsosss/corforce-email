-- Schema: templates | Service: templates
--
-- Correos de pedido para otros productos (docs/Plan_Plantillas_de_Pedido.md, fases 3 a 5):
--
-- * templates.key: clave estable de la plantilla dentro de la empresa (p. ej. pedido.confirmado).
--   Un producto que envia por el API la nombra por clave y no guarda un id distinto por empresa.
--   Opcional; unica por empresa cuando existe. Minusculas, digitos, _ y -, en segmentos separados
--   por punto, hasta 64 caracteres (lo repite el servicio).
-- * versions.markup: marcado estructurado que la plataforma anade al renderizar (hoy solo 'order',
--   la tarjeta de pedido de Gmail en JSON-LD). Es parte del contenido de la version: una publicada
--   no lo cambia, asi que entra en el trigger de inmutabilidad.
-- * brand_kits.image_hosts: servidores desde los que una variable de tipo imagen puede cargar una
--   imagen. Vacio: cualquier servidor https. Hasta 20 nombres de host.
--
-- Idempotente y aditiva: columnas nuevas nulas o con valor por defecto, restricciones que ninguna
-- fila existente incumple y la funcion del trigger reemplazada con una columna mas.

ALTER TABLE templates.templates ADD COLUMN IF NOT EXISTS key varchar(64);

ALTER TABLE templates.templates DROP CONSTRAINT IF EXISTS templates_key_check;
ALTER TABLE templates.templates ADD CONSTRAINT templates_key_check
    CHECK (key IS NULL OR key ~ '^[a-z][a-z0-9_-]*(\.[a-z][a-z0-9_-]*)*$');

CREATE UNIQUE INDEX IF NOT EXISTS templates_tenant_key_key
    ON templates.templates (tenant_id, key) WHERE key IS NOT NULL;

ALTER TABLE templates.versions ADD COLUMN IF NOT EXISTS markup varchar(20);

ALTER TABLE templates.versions DROP CONSTRAINT IF EXISTS versions_markup_check;
ALTER TABLE templates.versions ADD CONSTRAINT versions_markup_check
    CHECK (markup IS NULL OR markup IN ('order'));

CREATE OR REPLACE FUNCTION templates.versions_content_immutable()
RETURNS TRIGGER AS $$
BEGIN
    IF OLD.status <> 'draft' AND (
        NEW.subject IS DISTINCT FROM OLD.subject OR
        NEW.html IS DISTINCT FROM OLD.html OR
        NEW.text IS DISTINCT FROM OLD.text OR
        NEW.variables IS DISTINCT FROM OLD.variables OR
        NEW.editor IS DISTINCT FROM OLD.editor OR
        NEW.markup IS DISTINCT FROM OLD.markup) THEN
        RAISE EXCEPTION 'el contenido de una version publicada no cambia (version %)', OLD.id
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

ALTER TABLE templates.brand_kits ADD COLUMN IF NOT EXISTS image_hosts text[] NOT NULL DEFAULT '{}';

ALTER TABLE templates.brand_kits DROP CONSTRAINT IF EXISTS brand_kits_image_hosts_check;
ALTER TABLE templates.brand_kits ADD CONSTRAINT brand_kits_image_hosts_check
    CHECK (cardinality(image_hosts) <= 20);
