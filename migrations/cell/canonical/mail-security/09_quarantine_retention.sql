-- Schema: mail_security | Service: mail-security
--
-- Techos de la retencion de cuarentena por empresa: cuantos mensajes guarda la celda por buzon,
-- cuanto tiempo los conserva y cuantos dominios se excluyen.
--
-- 01 creo retention_size con CHECK (retention_size >= 0), max_age_days con CHECK (max_age_days > 0)
-- y exclude_domains sin ninguna restriccion: por el API una empresa podia fijar una retencion
-- practicamente infinita en la base COMPARTIDA de la celda. Cada fila de cuarentena lleva el
-- mensaje entero (quarantine.msg), la poda por buzon corre dentro de /pipe con cada mensaje que
-- entra, y los tres ajustes viajan ademas a Redis como el TOPE de la celda (Q_RETENTION_SIZE,
-- Q_MAX_AGE y Q_EXCLUDE_DOMAINS son el maximo entre empresas y la union de sus dominios), asi que
-- lo que pedia una empresa lo leian los motores de todas.
--
-- Los techos y el porque de cada numero viven en domain.MaxQuarantineRetentionSize,
-- MaxQuarantineMaxAgeDays y MaxQuarantineExcludeDomains;
-- services/mail-security/quarantine_limits_test.go ata estos CHECK a esas constantes, como
-- ops/scaffold/check-mail-size-limits.sh ata el techo de max_size_bytes de la migracion 08.
--
-- Las filas que ya los superaban se recortan antes de poner la restriccion. Aditiva e idempotente:
-- cada CHECK se reemplaza por nombre (DROP IF EXISTS y ADD con el mismo).

UPDATE mail_security.quarantine_settings
   SET retention_size = 2000
 WHERE retention_size > 2000;

UPDATE mail_security.quarantine_settings
   SET max_age_days = 730
 WHERE max_age_days > 730;

-- jsonb_array_length falla sobre lo que no es un array y el orden de evaluacion de AND no esta
-- garantizado, asi que el CASE decide primero. No hay filas que no sean un array (la columna nace
-- con DEFAULT '[]' y el servicio siempre escribe uno): el CASE esta para que la restriccion no
-- pueda romperse, no porque se espere ese caso.
UPDATE mail_security.quarantine_settings
   SET exclude_domains = jsonb_path_query_array(exclude_domains, '$[0 to 255]')
 WHERE jsonb_array_length(CASE WHEN jsonb_typeof(exclude_domains) = 'array'
                               THEN exclude_domains ELSE '[]'::jsonb END) > 256;

ALTER TABLE mail_security.quarantine_settings
    DROP CONSTRAINT IF EXISTS quarantine_settings_retention_size_check;
ALTER TABLE mail_security.quarantine_settings
    ADD CONSTRAINT quarantine_settings_retention_size_check
    CHECK (retention_size >= 0 AND retention_size <= 2000);

ALTER TABLE mail_security.quarantine_settings
    DROP CONSTRAINT IF EXISTS quarantine_settings_max_age_days_check;
ALTER TABLE mail_security.quarantine_settings
    ADD CONSTRAINT quarantine_settings_max_age_days_check
    CHECK (max_age_days > 0 AND max_age_days <= 730);

ALTER TABLE mail_security.quarantine_settings
    DROP CONSTRAINT IF EXISTS quarantine_settings_exclude_domains_check;
ALTER TABLE mail_security.quarantine_settings
    ADD CONSTRAINT quarantine_settings_exclude_domains_check
    CHECK (CASE WHEN jsonb_typeof(exclude_domains) = 'array'
                THEN jsonb_array_length(exclude_domains) <= 256 ELSE true END);
