-- Schema: scheduler | Service: scheduler
-- Zona horaria IANA por trabajo: la expresion cron se evalua en ella ("0 8 * * *" en
-- America/Lima son las 08:00 de Lima, no de UTC). No afecta a @every, a interval ni a
-- one_time, que cuentan tiempo transcurrido.
--
-- Las filas existentes toman 'UTC', que es como se evaluaban hasta ahora: ningun trabajo
-- cambia de hora. El servicio valida el nombre contra su base de zonas
-- (time.LoadLocation); aqui solo se impide el vacio, que no es ninguna zona.
-- Idempotente y solo aditiva.

ALTER TABLE scheduler.job_definitions ADD COLUMN IF NOT EXISTS timezone varchar(64) NOT NULL DEFAULT 'UTC';

DO $$
BEGIN
    ALTER TABLE scheduler.job_definitions ADD CONSTRAINT job_definitions_timezone_check
        CHECK (timezone <> '');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;
