-- Schema: transactional | Service: transactional
--
-- sending_ready: si Amazon SES acepta ya envios del dominio (VerifiedForSendingStatus de su
-- identidad), tal como lo anuncia domain-service en domains.domain.verified y
-- domains.domain.sending_status_changed cuando gestiona la identidad en SES. NULL: domain-service
-- no lo ha dicho (integracion de SES desactivada o fila anterior a esta migracion) y rige la regla
-- de siempre, verified con purpose sending o both. false bloquea el envio aunque el dominio este
-- verificado en su DNS.
--
-- Idempotente y aditiva: columna nueva nula, ninguna fila cambia de comportamiento al aplicarla.

ALTER TABLE transactional.sending_domains ADD COLUMN IF NOT EXISTS sending_ready boolean;
