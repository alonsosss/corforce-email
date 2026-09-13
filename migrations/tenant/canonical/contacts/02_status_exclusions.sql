-- Schema: contacts | Service: contacts
--
-- Estados invalid y excluded: el contacto cuya direccion tiene vigente en suppression una
-- causa invalid o manual (y ninguna mas grave) deja de figurar como active. Hasta aqui esas
-- dos causas no tenian estado en contacts: transactional no enviaba nada a la direccion,
-- pero el contacto seguia contando como enviable en segmentos, audiencias y en la consulta
-- interna de enviables.
--
-- Idempotente y aditiva: el CHECK nuevo admite los mismos valores que el anterior y dos
-- mas, asi que ninguna fila existente puede incumplirlo. Re-ejecutarla deja la misma
-- restriccion. No cambia ningun dato: el estado lo recalcula el servicio con las causas
-- vigentes de suppression (eventos y barridos).

ALTER TABLE contacts.contacts DROP CONSTRAINT IF EXISTS contacts_contacts_status_check;
ALTER TABLE contacts.contacts ADD CONSTRAINT contacts_contacts_status_check
    CHECK (status IN ('active', 'unsubscribed', 'bounced', 'complained', 'invalid', 'excluded'));
