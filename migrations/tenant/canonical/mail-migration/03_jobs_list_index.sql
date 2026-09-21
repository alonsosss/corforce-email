-- Schema: mail_migration | Service: mail-migration
--
-- Indice del listado de trabajos de la empresa (GET /migrations): ORDER BY created_at DESC, id sin filtro de
-- buzon. Los indices que existian cubren el reclamo (idx_mail_migration_jobs_active, solo pendientes y en
-- curso) y el listado de UN buzon (idx_mail_migration_jobs_mailbox); el listado general recorria la tabla
-- entera y la ordenaba (con 100000 trabajos terminados de una empresa, un recorrido paralelo de 21 ms en cada
-- pagina, sin importar cuantas filas devolviera). Con este indice cada pagina lee solo las suyas.
--
-- Sin CONCURRENTLY a proposito: el ejecutor de migraciones aplica cada fichero dentro de una transaccion y la
-- tabla es la del historial de migraciones de UNA empresa (decenas de filas), donde la construccion es
-- instantanea y el bloqueo de escritura no llega al milisegundo.
--
-- Idempotente y aditiva: se puede reejecutar sobre cualquier base de empresa.

CREATE INDEX IF NOT EXISTS idx_mail_migration_jobs_tenant_created
    ON mail_migration.jobs (tenant_id, created_at DESC, id);
