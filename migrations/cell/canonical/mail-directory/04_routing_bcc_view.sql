-- Schema: mail | Service: mail-directory
--
-- Vista publicada para mail-security: las copias ocultas que Rspamd aplica (endpoint
-- /bcc) se leen de aqui y no de la tabla. Columnas enumeradas; sin credenciales.
CREATE OR REPLACE VIEW mail.v_routing_bcc_maps AS
    SELECT tenant_id, local_dest, bcc_dest, domain, type, active FROM mail.bcc_maps;
GRANT SELECT ON mail.v_routing_bcc_maps TO mail_app;
