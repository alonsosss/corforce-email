import { useMemo, useState } from 'react';
import { accessApi, type Role } from '@/api/access';
import { errorMessage } from '@/api/messages';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useAction } from '@/hooks/useAction';
import { useQuery } from '@/hooks/useQuery';
import {
  Badge,
  Button,
  Card,
  EmptyState,
  ErrorState,
  Select,
  Skeleton,
  useToast,
} from '@/design/components';
import { IconPlus } from '@/design/icons';
import { t } from '@/i18n';

export function UserRolesCard({ userId }: { userId: string }) {
  const { can } = useAccess();
  const toast = useToast();
  const assigned = useQuery(async () => (await accessApi.getUserRoles(userId)).data, [userId]);
  const catalog = useQuery(async () => (await accessApi.listRoles()).data, []);
  const [selected, setSelected] = useState('');

  const available = useMemo(() => {
    const have = new Set((assigned.data ?? []).map((r) => r.id));
    return (catalog.data ?? []).filter((r) => !have.has(r.id));
  }, [assigned.data, catalog.data]);

  const assign = useAction(async () => {
    await accessApi.assignRole(userId, selected);
    setSelected('');
    assigned.reload();
  });

  const revoke = useAction(async (role: Role) => {
    await accessApi.revokeRole(userId, role.id);
    assigned.reload();
  });

  const canAssign = can(...PERMISSIONS.userRoles.assign);
  const canRevoke = can(...PERMISSIONS.userRoles.revoke);
  const error = assign.error ?? revoke.error;

  return (
    <Card title={t('users.roles.title')} description={t('users.roles.description')}>
      {assigned.error ? (
        <ErrorState error={assigned.error} onRetry={assigned.reload} />
      ) : assigned.loading && !assigned.data ? (
        <Skeleton lines={3} />
      ) : (
        <div className="cf-stack" style={{ gap: 'var(--cf-space-3)' }}>
          {(assigned.data ?? []).length === 0 ? (
            <EmptyState title={t('users.roles.empty')} />
          ) : (
            <ul
              style={{
                listStyle: 'none',
                display: 'flex',
                flexDirection: 'column',
                gap: 'var(--cf-space-2)',
              }}
            >
              {(assigned.data ?? []).map((role) => (
                <li
                  key={role.id}
                  style={{
                    display: 'flex',
                    alignItems: 'center',
                    justifyContent: 'space-between',
                    gap: 'var(--cf-space-3)',
                  }}
                >
                  <span className="cf-inline">
                    <strong>{role.name}</strong>
                    {role.is_system ? <Badge tone="accent">{t('roles.system')}</Badge> : null}
                    {role.description ? (
                      <span className="cf-text-muted cf-text-sm">{role.description}</span>
                    ) : null}
                  </span>
                  {canRevoke ? (
                    <Button
                      size="sm"
                      variant="ghost"
                      loading={revoke.busy}
                      onClick={() => {
                        void revoke.run(role).then((ok) => {
                          if (ok) toast.success(t('users.roles.revoked'));
                        });
                      }}
                    >
                      {t('users.roles.revoke')}
                    </Button>
                  ) : null}
                </li>
              ))}
            </ul>
          )}
          {canAssign ? (
            <div
              style={{
                display: 'flex',
                gap: 'var(--cf-space-2)',
                alignItems: 'center',
                flexWrap: 'wrap',
              }}
            >
              <div style={{ flex: '1 1 220px' }}>
                <Select
                  aria-label={t('users.roles.selectRole')}
                  placeholder={
                    available.length ? t('users.roles.selectRole') : t('users.roles.noneAvailable')
                  }
                  options={available.map((r) => ({ value: r.id, label: r.name }))}
                  value={selected}
                  onChange={(e) => setSelected(e.target.value)}
                  disabled={!available.length}
                />
              </div>
              <Button
                variant="primary"
                icon={<IconPlus size={16} />}
                disabled={!selected}
                loading={assign.busy}
                onClick={() => {
                  void assign.run().then((ok) => {
                    if (ok) toast.success(t('users.roles.assigned'));
                  });
                }}
              >
                {t('users.roles.assign')}
              </Button>
            </div>
          ) : null}
          {error ? (
            <div className="cf-form__error" role="alert">
              {errorMessage(error)}
            </div>
          ) : null}
        </div>
      )}
    </Card>
  );
}
