// Los dos unicos roles que existen en codigo (pkg/middleware/roles.go). Cualquier otro rol
// es un dato de access-control y la interfaz nunca lo compara por nombre.
export const SYSTEM_ROLES = {
  superadmin: 'superadmin',
  tenantAdmin: 'tenant_admin',
} as const;

export type SystemRole = (typeof SYSTEM_ROLES)[keyof typeof SYSTEM_ROLES];
