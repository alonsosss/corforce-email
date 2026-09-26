-- Schema: identity | Service: identity
-- Verificacion en dos pasos obligatoria por empresa (docs/Usuarios_Roles_y_Acceso.md):
--
--   require_mfa: con true, quien entra con contrasena y no tiene segundo factor no recibe
--     sesion; recibe un token de alta de 10 minutos que solo sirve para configurarlo
--     (POST /auth/mfa/enroll/setup y /activate) y la sesion se abre al activarlo. Con la
--     politica activa nadie de la empresa puede desactivar su segundo factor.
--
-- Idempotente y aditiva.

ALTER TABLE identity.session_policies ADD COLUMN IF NOT EXISTS require_mfa boolean NOT NULL DEFAULT false;
