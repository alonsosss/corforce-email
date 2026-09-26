import { identityApi, type User } from '@/api/identity';
import { PICKER_PAGE_SIZE } from '@/api/paging';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import type { SelectOption } from '@/design/components';
import { useQuery } from '@/hooks/useQuery';
import { fullName } from '@/lib/format';
import { t } from '@/i18n';

// Los servicios firman sus propias acciones (tareas del planificador, eventos internos) con
// el uuid nulo: no es una persona sino la plataforma.
export const SYSTEM_USER_ID = '00000000-0000-0000-0000-000000000000';

/**
 * Usuarios de la empresa para nombrar las filas que solo traen el id (auditoria, filtros de
 * sesiones). Sin permiso de lectura de usuarios, o si la lista falla, las filas muestran el id.
 */
export function useUserDirectory() {
  const { can } = useAccess();
  const allowed = can(...PERMISSIONS.users.read);
  const users = useQuery(
    async () =>
      allowed ? (await identityApi.listUsers({ page: 1, per_page: PICKER_PAGE_SIZE })).items : [],
    [allowed],
  );
  const list = users.data ?? [];
  const byId = new Map<string, User>(list.map((user) => [user.id, user]));
  const options: SelectOption[] = list.map((user) => ({
    value: user.id,
    label: `${fullName(user.first_name, user.last_name, user.email)} (${user.email})`,
  }));
  return { byId, options, available: allowed && users.data !== null };
}

export function UserLabel({ id, byId }: { id: string; byId: ReadonlyMap<string, User> }) {
  if (id === SYSTEM_USER_ID) return <span className="cf-text-muted">{t('user.system')}</span>;
  const user = byId.get(id);
  if (!user) return <span className="cf-mono cf-text-sm">{id}</span>;
  return (
    <div>
      <div>{fullName(user.first_name, user.last_name, user.email)}</div>
      <div className="cf-text-muted cf-text-sm">{user.email}</div>
    </div>
  );
}
