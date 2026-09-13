-- Schema: suppression | Service: suppression
--
-- Anuncio de la caducidad de una exclusion manual. Hasta aqui expires_at solo se comparaba
-- al leer: cuando una manual caducaba, la consulta previa al envio dejaba de devolverla sin
-- avisar a nadie, y contacts tenia que preguntar cada pocos minutos por todos sus contactos
-- excluidos. Ahora el barrido de este servicio publica suppression.entry.expired por la
-- outbox en la misma transaccion que anota aqui la caducidad anunciada.
--
-- announced_expires_at guarda el expires_at que ya se anuncio, no una marca booleana: una
-- manual renovada con otra caducidad, o reactivada sin ella, deja de coincidir con lo
-- anunciado y su caducidad nueva se anuncia a su vez, sin que ninguna escritura tenga que
-- acordarse de limpiar la marca.
--
-- Las filas existentes quedan con NULL: las manuales que caducaron antes de esta migracion
-- se anuncian en la primera pasada, y contacts las aplica de forma idempotente.
--
-- Idempotente y aditiva.

ALTER TABLE suppression.entries ADD COLUMN IF NOT EXISTS announced_expires_at timestamptz;

-- Solo las causas con una caducidad aun no anunciada (vigentes o ya caducadas): el barrido
-- recorre este indice, que se vacia de caducadas en cuanto se pone al dia.
CREATE INDEX IF NOT EXISTS idx_suppression_entries_expiry_pending
    ON suppression.entries (tenant_id, expires_at, id)
    WHERE expires_at IS NOT NULL AND announced_expires_at IS DISTINCT FROM expires_at;
