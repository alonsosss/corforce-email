-- Schema: mail_migration | Service: mail-migration
--
-- Credencial de destino por trabajo (docs/adr/0002, "Credencial de destino por trabajo"). Al reclamar
-- un trabajo el servicio genera 256 bits aleatorios, entrega al ejecutor el token una sola vez y guarda
-- aqui solo el SHA-256 del secreto. mail-auth pregunta al servicio si el token abre el buzon del trabajo
-- cada vez que Dovecot lo recibe; la respuesta sale de esta fila, asi que la credencial vale exactamente
-- lo que el trabajo esta en curso y con su lease vigente.
--
-- La restriccion impide que un trabajo pendiente o en estado final conserve credencial: todo cierre,
-- cancelacion y vencimiento la pone a NULL en la misma sentencia que cambia el estado.
--
-- Idempotente y aditiva: se puede reejecutar sobre cualquier base de empresa.

ALTER TABLE mail_migration.jobs
    ADD COLUMN IF NOT EXISTS destination_credential_hash bytea;

DO $$
BEGIN
    ALTER TABLE mail_migration.jobs
        ADD CONSTRAINT mail_migration_jobs_destination_credential_check CHECK (
            destination_credential_hash IS NULL
            OR (status = 'running' AND octet_length(destination_credential_hash) = 32)
        );
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;
