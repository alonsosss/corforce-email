-- Schema: mail | Service: mail-directory
--
-- Acceso DAV del buzon (CardDAV, docs/adr/0004): el flag que mail-auth comprueba cuando mail-dav le
-- pregunta por una credencial con service "dav". Como imap_access, pop3_access, smtp_access y
-- sieve_access, vale tanto para la contrasena principal como para las de aplicacion; estas ultimas ya
-- tenian su dav_access. Encendido por defecto, igual que el resto de protocolos: mail-dav solo existe en
-- los despliegues que lo activan. Idempotente y aditiva.

ALTER TABLE mail.mailboxes ADD COLUMN IF NOT EXISTS dav_access boolean NOT NULL DEFAULT true;
