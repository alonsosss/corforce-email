// Unico lugar donde viven las rutas del API. Cada ruta espeja un handler Go real:
// services/identity, access-control, organization y audit, servidos por el gateway bajo
// /api/v1 en el mismo origen que la aplicacion.

const API_PREFIX = '/api/v1';

const seg = (value: string): string => encodeURIComponent(value);

/**
 * Origen del API. Vacio por defecto: la aplicacion y el API comparten origen y las rutas
 * son relativas. VITE_API_URL solo se rellena cuando el API vive en otro host.
 */
export function apiBase(): string {
  const configured = (import.meta.env.VITE_API_URL ?? '').trim();
  return configured.replace(/\/+$/, '');
}

export const endpoints = {
  auth: {
    login: `${API_PREFIX}/auth/login`,
    refresh: `${API_PREFIX}/auth/refresh`,
    forgotPassword: `${API_PREFIX}/auth/forgot-password`,
    resetPassword: `${API_PREFIX}/auth/reset-password`,
    resetPasswordPolicy: `${API_PREFIX}/auth/reset-password/policy`,
    stepUp: `${API_PREFIX}/auth/step-up`,
    mfaChallenge: `${API_PREFIX}/auth/mfa/challenge`,
    mfaSetup: `${API_PREFIX}/auth/mfa/setup`,
    mfaActivate: `${API_PREFIX}/auth/mfa/activate`,
    mfaDisable: `${API_PREFIX}/auth/mfa/disable`,
  },
  users: {
    collection: `${API_PREFIX}/users`,
    byId: (id: string) => `${API_PREFIX}/users/${seg(id)}`,
    deactivate: (id: string) => `${API_PREFIX}/users/${seg(id)}/deactivate`,
    resetPassword: (id: string) => `${API_PREFIX}/users/${seg(id)}/reset-password`,
    changePassword: `${API_PREFIX}/users/change-password`,
    passwordPolicy: `${API_PREFIX}/users/password-policy`,
  },
  sessions: {
    logout: `${API_PREFIX}/sessions/logout`,
    logoutAll: `${API_PREFIX}/sessions/logout-all`,
    mine: `${API_PREFIX}/sessions/mine`,
    platform: `${API_PREFIX}/sessions/platform`,
    collection: `${API_PREFIX}/sessions`,
    policy: `${API_PREFIX}/sessions/policy`,
    byId: (id: string) => `${API_PREFIX}/sessions/${seg(id)}`,
  },
  roles: {
    collection: `${API_PREFIX}/roles`,
    byId: (id: string) => `${API_PREFIX}/roles/${seg(id)}`,
    permissions: (id: string) => `${API_PREFIX}/roles/${seg(id)}/permissions`,
  },
  permissions: {
    collection: `${API_PREFIX}/permissions`,
  },
  userRoles: {
    assign: `${API_PREFIX}/user-roles/assign`,
    revoke: `${API_PREFIX}/user-roles/revoke`,
    ofUser: (userId: string) => `${API_PREFIX}/user-roles/user/${seg(userId)}`,
  },
  access: {
    myModules: `${API_PREFIX}/access/my-modules`,
    denials: `${API_PREFIX}/access/denials`,
    policy: (userId: string) => `${API_PREFIX}/policy/${seg(userId)}`,
  },
  organizations: {
    collection: `${API_PREFIX}/organizations`,
    byId: (id: string) => `${API_PREFIX}/organizations/${seg(id)}`,
    modules: (id: string) => `${API_PREFIX}/organizations/${seg(id)}/modules`,
    migrate: (id: string) => `${API_PREFIX}/organizations/${seg(id)}/migrate`,
    reseedRoles: (id: string) => `${API_PREFIX}/organizations/${seg(id)}/reseed-roles`,
    migrateAll: `${API_PREFIX}/organizations/migrate`,
    migrationsStatus: `${API_PREFIX}/organizations/migrations/status`,
  },
  cells: {
    collection: `${API_PREFIX}/cells`,
    byId: (id: string) => `${API_PREFIX}/cells/${seg(id)}`,
  },
  audit: {
    logs: `${API_PREFIX}/audit/logs`,
    securityEvents: `${API_PREFIX}/audit/security-events`,
    acknowledge: (id: string) => `${API_PREFIX}/audit/security-events/${seg(id)}/acknowledge`,
    integrity: `${API_PREFIX}/audit/integrity`,
  },
} as const;
