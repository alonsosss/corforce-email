-- Schema: domains | Service: domain-service
--
-- Registros mta_sts (TXT _mta-sts.<dominio>, RFC 8461) y tls_rpt (TXT _smtp._tls.<dominio>,
-- RFC 8460) entre los que domain-service comprueba y guarda en dns_checks. Los dos son
-- recomendados, no requeridos: no cambian el estado de verificacion del dominio.
--
-- Idempotente y aditiva: el CHECK nuevo admite los mismos valores que el anterior y dos mas, asi que
-- ninguna fila existente puede incumplirlo. Re-ejecutarla deja la misma restriccion.

ALTER TABLE domains.dns_checks DROP CONSTRAINT IF EXISTS dns_checks_record_check;
ALTER TABLE domains.dns_checks ADD CONSTRAINT dns_checks_record_check
    CHECK (record IN ('ownership_txt', 'mx', 'spf', 'dkim', 'dkim_previous', 'dmarc', 'mta_sts', 'tls_rpt'));
