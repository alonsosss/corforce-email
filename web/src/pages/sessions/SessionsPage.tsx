import { useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { identityApi } from '@/api/identity';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { usePagination } from '@/hooks/usePagination';
import { useQuery } from '@/hooks/useQuery';
import { Card, Checkbox, Input, PageHeader, Tabs, useToast } from '@/design/components';
import { SessionsTable } from '@/pages/shared/SessionsTable';
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
        {isSuperadmin ? (
          <div className="cf-field">
            <label className="cf-field__label" htmlFor="sessions-tenant">
              {t('sessions.filter.tenant')}
            </label>
            <Input
              id="sessions-tenant"
              className="cf-mono"
              value={tenantId}
              onChange={(e) => {
                setTenantId(e.target.value);
                pager.reset();
              }}
            />
          </div>
        ) : null}
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
