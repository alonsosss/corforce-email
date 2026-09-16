-- Schema: mail_security | Service: mail-security
--
-- Techo del tamano de mensaje que una empresa guarda en cuarentena.
--
-- 01 creo max_size_bytes con CHECK (max_size_bytes > 0) y sin techo: por el API una empresa
-- podia fijar cualquier valor y quedarse con un ajuste que nunca se aplica, porque /pipe
-- recibe de Rspamd el mensaje entero y su cuerpo esta acotado por maxPipeMaxBodyMiB
-- (services/mail-security/main.go): 101 MiB, el message_size_limit de Postfix mas 1 MiB de
-- envoltorio multipart. Por encima de eso no llega ningun mensaje, asi que un max_size_bytes
-- mayor solo esconde el motivo real de que algo no quede en cuarentena.
--
-- domain.MaxQuarantineMaxSizeBytes lleva el mismo numero y ops/scaffold/check-mail-size-limits.sh
-- ata los tres. Las filas que ya lo superaban se bajan al techo antes de poner la restriccion.
-- Aditiva e idempotente: el CHECK se reemplaza por nombre (DROP IF EXISTS y ADD con el mismo).

UPDATE mail_security.quarantine_settings
   SET max_size_bytes = 105906176
 WHERE max_size_bytes > 105906176;

ALTER TABLE mail_security.quarantine_settings
    DROP CONSTRAINT IF EXISTS quarantine_settings_max_size_bytes_check;
ALTER TABLE mail_security.quarantine_settings
    ADD CONSTRAINT quarantine_settings_max_size_bytes_check
    CHECK (max_size_bytes > 0 AND max_size_bytes <= 105906176);
