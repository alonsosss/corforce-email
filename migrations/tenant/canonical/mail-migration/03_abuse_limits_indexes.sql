-- Schema: mail_migration | Service: mail-migration
--
-- Indices de los topes de abuso del alta de trabajos (docs/adr/0002): cuantos trabajos creo la empresa
-- en las ultimas 24 horas y cuantos terminaron con las credenciales de origen rechazadas contra un
-- mismo servidor y usuario en la ultima hora. Se consultan en cada alta, bajo el cerrojo de la empresa,
-- y el historial no se poda, asi que sin indice el coste crece con los trabajos acumulados.
--
-- Idempotente y aditiva.

CREATE INDEX IF NOT EXISTS idx_mail_migration_jobs_created
    ON mail_migration.jobs (tenant_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_mail_migration_jobs_auth_failures
    ON mail_migration.jobs (tenant_id, source_host, lower(source_username), finished_at)
    WHERE last_error_code = 'source_auth_failed';
