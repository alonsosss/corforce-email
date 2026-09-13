-- Schema: domains | Service: domain-service
--
-- Desactivacion pendiente en el directorio de la celda. Un dominio corporativo verificado que
-- cae a failed se desactiva en mail-directory de la celda de la empresa. Si esa llamada no
-- llega (organization sin respuesta, instancia caida o sin declarar, instancia de otra celda),
-- la marca queda y el barrido repite la desactivacion hasta que mail-directory la confirma: sin
-- ella el dominio seguiria activo en la celda. Se pone antes de llamar y se quita al confirmar.
--
-- Idempotente y aditiva: se puede reejecutar sobre cualquier base de empresa.

ALTER TABLE domains.domains ADD COLUMN IF NOT EXISTS directory_deactivation_pending boolean NOT NULL DEFAULT false;

-- El barrido busca las pendientes de cada empresa; casi nunca hay ninguna.
CREATE INDEX IF NOT EXISTS idx_domains_deactivation_pending ON domains.domains (tenant_id)
    WHERE directory_deactivation_pending;
