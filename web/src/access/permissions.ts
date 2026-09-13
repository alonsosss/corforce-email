import { MODULES } from './modules';

// Triples (module, resource, action) del catalogo sembrado en
// migrations/registry/005_seed_permissions.sql. La interfaz pregunta can() con estos
// valores; si el catalogo cambia, cambia aqui y en ningun otro sitio.
type Triple = readonly [module: string, resource: string, action: string];

export const PERMISSIONS = {
  users: {
    read: [MODULES.identity, 'users', 'read'],
    create: [MODULES.identity, 'users', 'create'],
    update: [MODULES.identity, 'users', 'update'],
    delete: [MODULES.identity, 'users', 'delete'],
  },
  sessions: {
    read: [MODULES.identity, 'sessions', 'read'],
    revoke: [MODULES.identity, 'sessions', 'revoke'],
  },
  sessionPolicies: {
    read: [MODULES.identity, 'session_policies', 'read'],
    update: [MODULES.identity, 'session_policies', 'update'],
  },
  roles: {
    read: [MODULES.access, 'roles', 'read'],
    create: [MODULES.access, 'roles', 'create'],
    update: [MODULES.access, 'roles', 'update'],
    delete: [MODULES.access, 'roles', 'delete'],
  },
  userRoles: {
    read: [MODULES.access, 'user_roles', 'read'],
    assign: [MODULES.access, 'user_roles', 'assign'],
    revoke: [MODULES.access, 'user_roles', 'revoke'],
  },
  denials: {
    read: [MODULES.access, 'denials', 'read'],
  },
  tenants: {
    read: [MODULES.organization, 'tenants', 'read'],
    create: [MODULES.organization, 'tenants', 'create'],
    update: [MODULES.organization, 'tenants', 'update'],
    delete: [MODULES.organization, 'tenants', 'delete'],
  },
  tenantModules: {
    read: [MODULES.organization, 'modules', 'read'],
    update: [MODULES.organization, 'modules', 'update'],
  },
  migrations: {
    read: [MODULES.organization, 'migrations', 'read'],
    run: [MODULES.organization, 'migrations', 'run'],
  },
  auditLogs: {
    read: [MODULES.audit, 'logs', 'read'],
  },
  securityEvents: {
    read: [MODULES.audit, 'security_events', 'read'],
    acknowledge: [MODULES.audit, 'security_events', 'acknowledge'],
  },
  integrity: {
    read: [MODULES.audit, 'integrity', 'read'],
    verify: [MODULES.audit, 'integrity', 'verify'],
  },
} as const satisfies Record<string, Record<string, Triple>>;

export type PermissionTripleConst = Triple;
