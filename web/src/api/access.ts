import { api } from './client';
import { endpoints } from './endpoints';

// DTOs de services/access-control/internal/adapters/http/handler.go.

export interface Role {
  id: string;
  name: string;
  description: string;
  is_system: boolean;
  status: string;
  created_at: string;
  updated_at: string;
}

export interface Permission {
  id: string;
  module: string;
  resource: string;
  action: string;
  description: string;
}

export interface PermissionTriple {
  module: string;
  resource: string;
  action: string;
}

export interface RoleInput {
  name: string;
  description: string;
}

export interface MyModules {
  is_admin: boolean;
  roles: string[];
  modules: string[];
  write_modules: string[] | null;
  write_actions: Record<string, string[]> | null;
  disabled_modules: string[];
  tokens_valid_from: string;
}

export interface AccessPolicy {
  user_id: string;
  tenant_id: string;
  roles: string[];
  permissions: PermissionTriple[];
}

export interface DenialSummaryRow {
  key: string;
  count: number;
}

export interface DenialRecord {
  user_id: string;
  module: string;
  action: string;
  method: string;
  path: string;
  enforced: boolean;
  created_at: string;
}

export interface DenialMetrics {
  days: number;
  total: number;
  by_module: DenialSummaryRow[];
  by_user: DenialSummaryRow[];
  recent: DenialRecord[];
}

export interface DenialsQuery {
  days?: number;
  limit?: number;
}

interface StatusResponse {
  status: string;
}

export const accessApi = {
  listRoles: () => api.get<Role[]>(endpoints.roles.collection),
  getRole: (id: string) => api.get<Role>(endpoints.roles.byId(id)),
  createRole: (input: RoleInput) => api.post<Role>(endpoints.roles.collection, { body: input }),
  updateRole: (id: string, input: RoleInput) =>
    api.put<Role>(endpoints.roles.byId(id), { body: input }),
  deleteRole: (id: string) => api.delete<StatusResponse>(endpoints.roles.byId(id)),

  getRolePermissions: (id: string) => api.get<Permission[]>(endpoints.roles.permissions(id)),
  setRolePermissions: (id: string, permissionIds: string[]) =>
    api.put<StatusResponse>(endpoints.roles.permissions(id), {
      body: { permission_ids: permissionIds },
    }),

  listPermissions: (module?: string) =>
    api.get<Permission[]>(endpoints.permissions.collection, { params: { module } }),

  getUserRoles: (userId: string) => api.get<Role[]>(endpoints.userRoles.ofUser(userId)),
  assignRole: (userId: string, roleId: string) =>
    api.post<StatusResponse>(endpoints.userRoles.assign, {
      body: { user_id: userId, role_id: roleId },
    }),
  revokeRole: (userId: string, roleId: string) =>
    api.post<StatusResponse>(endpoints.userRoles.revoke, {
      body: { user_id: userId, role_id: roleId },
    }),

  myModules: () => api.get<MyModules>(endpoints.access.myModules),
  policy: (userId: string) => api.get<AccessPolicy>(endpoints.access.policy(userId)),
  denials: (query: DenialsQuery) =>
    api.get<DenialMetrics>(endpoints.access.denials, { params: { ...query } }),
};
