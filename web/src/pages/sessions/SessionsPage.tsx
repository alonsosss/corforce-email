import { useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { identityApi } from '@/api/identity';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { usePagination } from '@/hooks/usePagination';
import { useQuery } from '@/hooks/useQuery';
import { Card, Checkbox, Input, PageHeader, Select, Tabs, useToast } from '@/design/components';
import { SessionsTable } from '@/pages/shared/SessionsTable';
import { useTenantDirectory } from '@/pages/shared/useTenantDirectory';
import { useUserDirectory } from '@/pages/shared/useUserDirectory';
import { t } from '@/i18n';
import { SessionPolicyForm } from './SessionPolicyForm';

const TABS = [
  { id: 'sessions', label: t('sessions.tab.sessions') },
  { id: 'policy', label: t('sessions.tab.policy') },
] as const;

type TabId = (typeof TABS)[number]['id'];

export default function SessionsPage() {
  const [params, setParams] = useSearchParams();
  const tab: TabId = params.get('tab') === 'policy' ? 'policy' : 'sessions';

  return (
    <div>
      <PageHeader title={t('sessions.title')} description={t('sessions.subtitle')} />
      <Tabs
        items={TABS}
        value={tab}
        onChange={(id) => setParams({ tab: id })}
        label={t('sessions.title')}
      />
      {tab === 'sessions' ? <SessionsList /> : <SessionPolicyForm />}
    </div>
  );
}

function SessionsList() {
  const toast = useToast();
  const { isSuperadmin, can } = useAccess();
  const pager = usePagination();
  const [includeRevoked, setIncludeRevoked] = useState(false);
  const [userId, setUserId] = useState('');
  const [tenantId, setTenantId] = useState('');

  // El superadmin ve todas las empresas (GET /sessions/platform); el administrador de
  // empresa, la suya (GET /sessions).
  const sessions = useQuery(() => {
    const query = {
      page: pager.page,
      per_page: pager.perPage,
      active: includeRevoked ? false : undefined,
      user_id: userId.trim() || undefined,
    };
    return isSuperadmin
      ? identityApi.listPlatformSessions({ ...query, tenant_id: tenantId.trim() || undefined })
      : identityApi.listSessions(query);
  }, [pager.page, pager.perPage, includeRevoked, userId, tenantId, isSuperadmin]);

  const canRevoke = can(...PERMISSIONS.sessions.revoke);

  return (
    <Card flush description={isSuperadmin ? t('sessions.platformView') : undefined}>
      <div className="cf-toolbar">
        {isSuperadmin ? (
          <>
            <div className="cf-field">
              <label className="cf-field__label" htmlFor="sessions-user">
                {t('sessions.filter.user')}
              </label>
              <Input
                id="sessions-user"
                className="cf-mono"
                value={userId}
                onChange={(e) => {
                  setUserId(e.target.value);
                  pager.reset();
                }}
              />
            </div>
            <TenantFilter
              value={tenantId}
              onChange={(id) => {
                setTenantId(id);
                pager.reset();
              }}
            />
          </>
        ) : (
          <UserFilter
            value={userId}
            onChange={(id) => {
              setUserId(id);
              pager.reset();
            }}
          />
        )}
        <Checkbox
          label={t('session.showRevoked')}
          checked={includeRevoked}
          onChange={(e) => {
            setIncludeRevoked(e.target.checked);
            pager.reset();
          }}
        />
      </div>
      <SessionsTable
        rows={sessions.data?.items ?? []}
        loading={sessions.loading}
        error={sessions.error}
        onRetry={sessions.reload}
        showUser
        showTenant={isSuperadmin}
        pagination={{
          page: sessions.data?.page ?? pager.page,
          perPage: pager.perPage,
          total: sessions.data?.total ?? 0,
          totalPages: sessions.data?.totalPages ?? 0,
          onPageChange: pager.setPage,
        }}
        onRevoke={
          canRevoke
            ? async (s) => {
                await identityApi.revokeSession(s.id);
                toast.success(t('session.revoked'));
                sessions.reload();
              }
            : undefined
        }
      />
    </Card>
  );
}

interface FilterProps {
  value: string;
  onChange: (id: string) => void;
}

/** La empresa se elige de la lista; si no se puede leer, se escribe su id. */
function TenantFilter({ value, onChange }: FilterProps) {
  const { tenants } = useTenantDirectory();
  return (
    <div className="cf-field">
      <label className="cf-field__label" htmlFor="sessions-tenant">
        {t(tenants.length ? 'sessions.filter.tenantName' : 'sessions.filter.tenant')}
      </label>
      {tenants.length ? (
        <Select
          id="sessions-tenant"
          value={value}
          placeholder={t('common.all')}
          options={tenants.map((tenant) => ({
            value: tenant.id,
            label: `${tenant.name} (${tenant.slug})`,
          }))}
          onChange={(e) => onChange(e.target.value)}
        />
      ) : (
        <Input
          id="sessions-tenant"
          className="cf-mono"
          value={value}
          onChange={(e) => onChange(e.target.value)}
        />
      )}
    </div>
  );
}

/** El usuario de la empresa se elige de la lista; sin permiso de usuarios, se escribe su id. */
function UserFilter({ value, onChange }: FilterProps) {
  const users = useUserDirectory();
  return (
    <div className="cf-field">
      <label className="cf-field__label" htmlFor="sessions-user">
        {t(users.available ? 'sessions.filter.userName' : 'sessions.filter.user')}
      </label>
      {users.available ? (
        <Select
          id="sessions-user"
          value={value}
          placeholder={t('common.all')}
          options={users.options}
          onChange={(e) => onChange(e.target.value)}
        />
      ) : (
        <Input
          id="sessions-user"
          className="cf-mono"
          value={value}
          onChange={(e) => onChange(e.target.value)}
        />
      )}
    </div>
  );
}
