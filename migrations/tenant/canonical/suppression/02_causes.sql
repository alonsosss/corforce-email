-- Schema: suppression | Service: suppression
--
-- Una fila por causa, no por direccion. Hasta aqui la unicidad era (tenant_id, email) y
-- la fila guardaba solo la causa mas grave: si una direccion se daba de baja despues de
-- que la empresa la excluyera a mano, la exclusion manual desaparecia dentro de la baja,
-- y al levantar la baja la direccion quedaba libre aunque la empresa no la hubiera
-- liberado. Ahora cada causa (hard_bounce, complaint, unsubscribe, invalid, manual) es su
-- propia fila, con su origen, su detalle y su caducidad, y se registra y se retira por
-- separado. La causa principal de una direccion (la vigente mas grave) la calcula el
-- servicio al leer; no se guarda.
--
-- Datos existentes: cada fila del modelo anterior ya es la fila de su causa (conserva id,
-- origen, detalle, mensaje, campana, caducidad y fechas), asi que no se mueve ni se
-- reescribe ninguna. Las causas menores que el modelo anterior absorbio nunca se
-- guardaron y no se pueden reconstruir.
--
-- Idempotente y aditiva en datos: primero se crea la unicidad nueva (la antigua la
-- implica, asi que nunca falla sobre datos existentes) y despues se retira la antigua. En
-- ningun momento la tabla queda sin unicidad.

DO $$
BEGIN
    ALTER TABLE suppression.entries
        ADD CONSTRAINT suppression_entries_tenant_email_reason_key UNIQUE (tenant_id, email, reason);
EXCEPTION WHEN duplicate_table OR duplicate_object THEN NULL;
END $$;

ALTER TABLE suppression.entries DROP CONSTRAINT IF EXISTS suppression_entries_tenant_email_key;
