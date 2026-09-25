-- Schema: domains | Service: domain-service
--
-- Identidades de Amazon SES de los dominios que envian por SES (purpose sending o both).
-- domain-service da de alta cada uno al verificarse, firmando con su misma clave DKIM (BYODKIM), con
-- su subdominio MAIL FROM (bounce.<dominio>) y el conjunto transaccional por defecto, y el barrido
-- guarda aqui lo que SES dice de el. Solo con ses_identity_status = verified (VerifiedForSendingStatus
-- de SES) llega a transactional como apto para enviar.
--
-- ses_identity_status: pending, verified o failed; NULL si el dominio no tiene identidad en SES.
-- ses_dkim_status y ses_mail_from_status: el estado de SES en minusculas; NULL si no lo hay o si SES
-- devolvio un valor que esta version no conoce. ses_checked_at: la ultima consulta que respondio.
-- ses_last_error: el ultimo fallo al sincronizar (NULL si la ultima fue bien); el barrido reintenta.
--
-- dns_checks admite los registros del MAIL FROM (ses_mail_from_mx y ses_mail_from_spf), recomendados:
-- no cambian el estado de verificacion del dominio.
--
-- Idempotente y aditiva: columnas nuevas nulas y un CHECK de dns_checks que admite los mismos valores
-- que el anterior y dos mas, asi que ninguna fila existente incumple nada.

ALTER TABLE domains.domains ADD COLUMN IF NOT EXISTS ses_identity_status varchar(20);
ALTER TABLE domains.domains ADD COLUMN IF NOT EXISTS ses_dkim_status varchar(20);
ALTER TABLE domains.domains ADD COLUMN IF NOT EXISTS ses_mail_from_status varchar(20);
ALTER TABLE domains.domains ADD COLUMN IF NOT EXISTS ses_checked_at timestamptz;
ALTER TABLE domains.domains ADD COLUMN IF NOT EXISTS ses_last_error text;

ALTER TABLE domains.domains DROP CONSTRAINT IF EXISTS domains_ses_identity_status_check;
ALTER TABLE domains.domains ADD CONSTRAINT domains_ses_identity_status_check
    CHECK (ses_identity_status IN ('pending', 'verified', 'failed'));
ALTER TABLE domains.domains DROP CONSTRAINT IF EXISTS domains_ses_dkim_status_check;
ALTER TABLE domains.domains ADD CONSTRAINT domains_ses_dkim_status_check
    CHECK (ses_dkim_status IN ('pending', 'success', 'failed', 'temporary_failure', 'not_started'));
ALTER TABLE domains.domains DROP CONSTRAINT IF EXISTS domains_ses_mail_from_status_check;
ALTER TABLE domains.domains ADD CONSTRAINT domains_ses_mail_from_status_check
    CHECK (ses_mail_from_status IN ('pending', 'success', 'failed', 'temporary_failure', 'not_started'));
ALTER TABLE domains.domains DROP CONSTRAINT IF EXISTS domains_ses_last_error_length;
ALTER TABLE domains.domains ADD CONSTRAINT domains_ses_last_error_length
    CHECK (char_length(ses_last_error) <= 500);

-- El barrido reintenta los dominios no verificados cuya ultima sincronizacion con SES fallo.
CREATE INDEX IF NOT EXISTS idx_domains_ses_pending ON domains.domains (tenant_id)
    WHERE ses_last_error IS NOT NULL;

-- Solo si la restriccion vigente aun no admite los registros de SES: re-ejecutarla sobre una base con una
-- restriccion posterior mas amplia no la estrecha.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
         WHERE conname = 'dns_checks_record_check' AND conrelid = 'domains.dns_checks'::regclass
           AND pg_get_constraintdef(oid) LIKE '%ses_mail_from_spf%'
    ) THEN
        ALTER TABLE domains.dns_checks DROP CONSTRAINT IF EXISTS dns_checks_record_check;
        ALTER TABLE domains.dns_checks ADD CONSTRAINT dns_checks_record_check
            CHECK (record IN ('ownership_txt', 'mx', 'spf', 'dkim', 'dkim_previous', 'dmarc', 'mta_sts', 'tls_rpt',
                              'ses_mail_from_mx', 'ses_mail_from_spf'));
    END IF;
END $$;
