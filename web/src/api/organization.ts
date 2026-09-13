import { api } from './client';
import { endpoints } from './endpoints';
import { toPage, type Page, type PageQuery } from './types';

// DTOs de services/organization/internal/adapters/http/{handler,cells_handler}.go.

export type TenantStatus = 'active' | 'suspended' | 'inactive';
export type CellStatus = 'active' | 'draining' | 'closed';

export const TENANT_STATUSES: readonly TenantStatus[] = ['active', 'suspended', 'inactive'];
export const CELL_STATUSES: readonly CellStatus[] = ['active', 'draining', 'closed'];

// Ajustes descriptivos admitidos por app.TenantSettingKeys(), en el mismo orden.
export const TENANT_SETTING_KEYS = [
  'legal_name',
  'tz',
  'phone',
  'address',
  'website',
  'logo_url',
] as const;

export type TenantSettingKey = (typeof TENANT_SETTING_KEYS)[number];

export type TenantSettings = Partial<Record<TenantSettingKey, string>>;

export interface Tenant extends TenantSettings {
  id: string;
  slug: string;
  name: string;
  status: TenantStatus;
  cell_id: string;
  created_at: string;
  updated_at: string;
}

export interface CreateTenantRequest {
  slug: string;
  name: string;
  cell_code?: string;
  settings?: Record<string, string>;
  admin_email: string;
  admin_password: string;
  admin_first_name?: string;
  admin_last_name?: string;
}

export interface UpdateTenantRequest {
  name?: string;
  status?: TenantStatus;
  settings?: Record<string, string | null>;
}

export interface ModuleInfo {
  module: string;
  tier: string;
  requires: string[] | null;
  label: string;
  permission_modules: string[] | null;
  enabled: boolean;
}

export interface MigrationSweepResult {
  migrated: number;
  failed: number;
  locked: number;
}

export type MigrationStatus = 'ok' | 'pending' | 'error';

export interface TenantMigrationInfo {
  tenant_id: string;
  slug: string;
  db_name: string;
  status: MigrationStatus;
  applied: number;
  pending: string[] | null;
  baselined: boolean;
  error?: string;
}

export interface Cell {
  id: string;
  code: string;
  region: string;
  status: CellStatus;
  db_host: string;
  db_port: number;
  created_at: string;
}

export interface CreateCellRequest {
  code: string;
  region: string;
  db_host: string;
  db_port: number;
}

export interface UpdateCellRequest {
  status?: CellStatus;
  region?: string;
}

interface StatusResponse {
  status: string;
}

const DEFAULT_PAGE = { page: 1, per_page: 20 } as const;

export const organizationApi = {
  listTenants: async (query: PageQuery): Promise<Page<Tenant>> =>
    toPage(await api.get<Tenant[]>(endpoints.organizations.collection, { params: { ...query } }), {
      page: query.page ?? DEFAULT_PAGE.page,
      per_page: query.per_page ?? DEFAULT_PAGE.per_page,
    }),
  getTenant: (id: string) => api.get<Tenant>(endpoints.organizations.byId(id)),
  createTenant: (input: CreateTenantRequest) =>
    api.post<Tenant>(endpoints.organizations.collection, { body: input }),
  updateTenant: (id: string, input: UpdateTenantRequest) =>
    api.patch<Tenant>(endpoints.organizations.byId(id), { body: input }),
  deleteTenant: (id: string) => api.delete<null>(endpoints.organizations.byId(id)),
  reseedRoles: (id: string) => api.post<StatusResponse>(endpoints.organizations.reseedRoles(id)),

  getModules: (id: string) => api.get<ModuleInfo[]>(endpoints.organizations.modules(id)),
  setModule: (id: string, module: string, enabled: boolean) =>
    api.put<ModuleInfo[]>(endpoints.organizations.modules(id), { body: { module, enabled } }),

  migrationsStatus: () =>
    api.get<{ tenants: TenantMigrationInfo[] }>(endpoints.organizations.migrationsStatus),
  migrateTenant: (id: string) => api.post<StatusResponse>(endpoints.organizations.migrate(id)),
  migrateAll: () => api.post<MigrationSweepResult>(endpoints.organizations.migrateAll),

  listCells: () => api.get<Cell[]>(endpoints.cells.collection),
  createCell: (input: CreateCellRequest) =>
    api.post<Cell>(endpoints.cells.collection, { body: input }),
  updateCell: (id: string, input: UpdateCellRequest) =>
    api.patch<Cell>(endpoints.cells.byId(id), { body: input }),
};
