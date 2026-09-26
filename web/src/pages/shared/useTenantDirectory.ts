import { organizationApi, type Tenant } from '@/api/organization';
import { PICKER_PAGE_SIZE } from '@/api/paging';
import { useQuery } from '@/hooks/useQuery';

/**
 * Empresas de la plataforma para nombrar las filas de las pantallas del superadmin
 * (billing y reputation solo devuelven el id). Si falla, las filas muestran el id.
 */
export function useTenantDirectory() {
  const tenants = useQuery(
    async () => (await organizationApi.listTenants({ page: 1, per_page: PICKER_PAGE_SIZE })).items,
    [],
  );
  const byId = new Map<string, Tenant>((tenants.data ?? []).map((tenant) => [tenant.id, tenant]));
  return { tenants: tenants.data ?? [], byId };
}

export function tenantLabel(byId: Map<string, Tenant>, id: string): string {
  const tenant = byId.get(id);
  return tenant ? `${tenant.name} (${tenant.slug})` : id;
}
