import { organizationApi, type ModuleInfo } from '@/api/organization';
import { errorMessage } from '@/api/messages';
import { useAction } from '@/hooks/useAction';
import { useQuery } from '@/hooks/useQuery';
import { Badge, Card, Checkbox, DataTable, useToast, type Column } from '@/design/components';
import { t } from '@/i18n';

const TIER_CORE = 'core';

export function OrganizationModules({
  tenantId,
  editable,
}: {
  tenantId: string;
  editable: boolean;
}) {
  const toast = useToast();
  const modules = useQuery(
    async () => (await organizationApi.getModules(tenantId)).data,
    [tenantId],
  );

  const toggle = useAction(async (module: string, enabled: boolean) => {
    const { data } = await organizationApi.setModule(tenantId, module, enabled);
    modules.setData(data);
  });

  const columns: Column<ModuleInfo>[] = [
    {
      key: 'enabled',
      header: t('common.status'),
      width: '1%',
      render: (m) => (
        <Checkbox
          aria-label={m.label}
          checked={m.enabled}
          disabled={!editable || m.tier === TIER_CORE || toggle.busy}
          onChange={(e) => {
            void toggle.run(m.module, e.target.checked).then((ok) => {
              if (ok) toast.success(t('orgs.modules.updated'));
            });
          }}
        />
      ),
    },
    {
      key: 'label',
      header: t('common.name'),
      render: (m) => (
        <div>
          <div>
            <strong>{m.label}</strong>
          </div>
          <div className="cf-mono cf-text-muted cf-text-sm">{m.module}</div>
        </div>
      ),
    },
    {
      key: 'tier',
      header: t('orgs.modules.tier'),
      render: (m) =>
        m.tier === TIER_CORE ? (
          <Badge tone="accent">{t('orgs.modules.core')}</Badge>
        ) : (
          <Badge>{m.tier}</Badge>
        ),
    },
    {
      key: 'requires',
      header: t('orgs.modules.requires'),
      render: (m) =>
        m.requires?.length ? (
          <span className="cf-mono">{m.requires.join(', ')}</span>
        ) : (
          t('common.dash')
        ),
    },
    {
      key: 'perms',
      header: t('orgs.modules.permissionModules'),
      render: (m) =>
        m.permission_modules?.length ? (
          <span className="cf-mono">{m.permission_modules.join(', ')}</span>
        ) : (
          t('common.dash')
        ),
    },
  ];

  return (
    <Card title={t('orgs.modules.title')} description={t('orgs.modules.description')} flush>
      <DataTable
        columns={columns}
        rows={modules.data ?? []}
        rowKey={(m) => m.module}
        loading={modules.loading}
        error={modules.error}
        onRetry={modules.reload}
        empty={{ title: t('orgs.modules.empty') }}
      />
      {toggle.error ? (
        <div className="cf-card__body">
          <div className="cf-form__error" role="alert">
            {errorMessage(toggle.error)}
          </div>
        </div>
      ) : null}
    </Card>
  );
}
