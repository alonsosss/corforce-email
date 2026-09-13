-- Schema: organization | Service: organization
--
-- Indice global de los dominios de correo activos: dominio -> empresa. Una fila por dominio
-- activo en el directorio de la celda de su empresa. domain-service la reclama por la API
-- interna de organization antes de activar el dominio en mail-directory y la suelta despues de
-- desactivarlo; las dos llamadas son idempotentes y el barrido de domain-service las repite, que
-- es lo que hace converger el indice (tambien para los dominios activados antes de que existiera).
--
-- La celda no se copia: sale de la empresa (organization.tenants.cell_id), asi que un traslado de
-- empresa lleva sus dominios sin una segunda escritura. La clave primaria es la unicidad de los
-- dominios activos entre celdas, que dentro de una celda ya garantiza mail.name_in_use. El gateway
-- la consulta por GET /internal/organization/mail-domains/{dominio}/cell para llevar el inicio
-- de sesion del webmail a la celda del buzon (Modelo_de_Datos_y_Celdas.md, 5.5). Nadie mas la
-- lee: no hay vista publicada. Cae con la empresa. Idempotente.

CREATE TABLE IF NOT EXISTS organization.mail_domain_cells (
    domain     varchar(253) PRIMARY KEY CHECK (domain <> '' AND domain = lower(domain)),
    tenant_id  uuid NOT NULL REFERENCES organization.tenants (id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT NOW(),
    updated_at timestamptz NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_mail_domain_cells_tenant ON organization.mail_domain_cells (tenant_id);

CREATE OR REPLACE TRIGGER trg_mail_domain_cells_updated_at BEFORE UPDATE ON organization.mail_domain_cells
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();
