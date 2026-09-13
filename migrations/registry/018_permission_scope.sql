-- Schema: access_control | Service: access-control
--
-- Alcance de cada permiso del catalogo. 'platform' marca lo que solo opera el superadmin
-- (empresas, catalogo de planes, suscripciones de todas las empresas): nunca se asigna a
-- un rol de empresa, ni al sembrar el tenant_admin ni al editar un rol por API. El
-- superadmin no necesita estos permisos en ningun rol: pasa por ser rol del sistema.
--
-- Una migracion posterior que siembre un permiso de plataforma lo declara con
-- scope = 'platform' en su propio INSERT. Idempotente.

ALTER TABLE access_control.permissions
    ADD COLUMN IF NOT EXISTS scope varchar(20) NOT NULL DEFAULT 'tenant';

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
         WHERE conname = 'permissions_scope_check'
           AND conrelid = 'access_control.permissions'::regclass
    ) THEN
        ALTER TABLE access_control.permissions
            ADD CONSTRAINT permissions_scope_check CHECK (scope IN ('tenant', 'platform'));
    END IF;
END $$;

UPDATE access_control.permissions
   SET scope = 'platform'
 WHERE scope <> 'platform'
   AND (module = 'organization'
        OR (module = 'billing' AND resource IN ('plans', 'subscriptions'))
        OR (module = 'reputation' AND resource = 'tenants'));

-- Los roles de empresa sembrados antes de existir el alcance recibieron permisos de
-- plataforma: se retiran.
DELETE FROM access_control.role_permissions rp
 USING access_control.permissions p
 WHERE rp.permission_id = p.id
   AND p.scope = 'platform';
