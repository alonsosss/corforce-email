import { useEffect, useState, type FormEvent } from 'react';
import { useNavigate, useParams, useSearchParams } from 'react-router-dom';
import {
  organizationApi,
  TENANT_SETTING_KEYS,
  TENANT_STATUSES,
  type Tenant,
  type TenantSettingKey,
  type TenantStatus,
} from '@/api/organization';
import { errorMessage } from '@/api/messages';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useAction } from '@/hooks/useAction';
import { useQuery } from '@/hooks/useQuery';
import {
  Badge,
  Button,
  Card,
  ConfirmDialog,
  DescriptionList,
  EmptyState,
  ErrorState,
  FormField,
  Input,
  PageHeader,
  Select,
  Skeleton,
  Tabs,
  useToast,
} from '@/design/components';
import { IconTrash } from '@/design/icons';
import { formatDateTime } from '@/lib/format';
import { t, tEnum, type MessageKey } from '@/i18n';
import { paths } from '@/paths';
import { tenantStatusTone } from './OrganizationsPage';
import { OrganizationModules } from './OrganizationModules';
import { MigrationsTable } from './MigrationsTable';

const TABS = [
  { id: 'general', label: t('orgs.detail.tab.general') },
  { id: 'modules', label: t('orgs.detail.tab.modules') },
  { id: 'migrations', label: t('orgs.detail.tab.migrations') },
] as const;

type TabId = (typeof TABS)[number]['id'];

function isTab(value: string | null): value is TabId {
  return TABS.some((tab) => tab.id === value);
}

export default function OrganizationDetailPage() {
  const { id = '' } = useParams();
  const [params, setParams] = useSearchParams();
  const { isSuperadmin } = useAccess();
  const tenant = useQuery(async () => (await organizationApi.getTenant(id)).data, [id]);

  // Modulos y migraciones son operaciones de plataforma: solo el superadmin las ve.
  const tabs = isSuperadmin ? TABS : TABS.filter((tab) => tab.id === 'general');
  const raw = params.get('tab');
  const tab: TabId = isTab(raw) && tabs.some((x) => x.id === raw) ? raw : 'general';

  if (tenant.error) {
    return (
      <div>
        <PageHeader
          title={t('orgs.title')}
          back={{ to: paths.organizations, label: t('nav.organizations') }}
        />
        <Card>
          <ErrorState error={tenant.error} title={t('orgs.notFound')} onRetry={tenant.reload} />
        </Card>
      </div>
    );
  }
  if (!tenant.data) {
    return (
      <Card>
        <Skeleton lines={5} />
      </Card>
    );
  }

  const o = tenant.data;
  return (
    <div>
      <PageHeader
        title={o.name}
        description={<span className="cf-mono">{o.slug}</span>}
        back={{ to: paths.organizations, label: t('nav.organizations') }}
      />
      {tabs.length > 1 ? (
        <Tabs
          items={tabs}
          value={tab}
          onChange={(next) => setParams({ tab: next })}
          label={o.name}
        />
      ) : null}
      {tab === 'general' ? <GeneralTab tenant={o} onChange={tenant.setData} /> : null}
      {tab === 'modules' ? <OrganizationModules tenantId={o.id} editable={isSuperadmin} /> : null}
      {tab === 'migrations' ? <MigrationsTab tenantId={o.id} /> : null}
    </div>
  );
}

function settingLabel(key: TenantSettingKey): string {
  return t(`orgs.setting.${key}` as MessageKey);
}

function GeneralTab({ tenant, onChange }: { tenant: Tenant; onChange: (next: Tenant) => void }) {
  const navigate = useNavigate();
  const toast = useToast();
  const { isSuperadmin, can } = useAccess();
  const [name, setName] = useState(tenant.name);
  const [status, setStatus] = useState<TenantStatus>(tenant.status);
  const [settings, setSettings] = useState<Record<TenantSettingKey, string>>(() =>
    readSettings(tenant),
  );
  const [deleting, setDeleting] = useState(false);

  useEffect(() => {
    setName(tenant.name);
    setStatus(tenant.status);
    setSettings(readSettings(tenant));
  }, [tenant]);

  const editable = isSuperadmin && can(...PERMISSIONS.tenants.update);

  const save = useAction(async () => {
    const changedSettings: Record<string, string | null> = {};
    for (const key of TENANT_SETTING_KEYS) {
      const next = settings[key].trim();
      const current = tenant[key] ?? '';
      if (next !== current) changedSettings[key] = next || null;
    }
    const body = {
      name: name.trim() !== tenant.name ? name.trim() : undefined,
      status: status !== tenant.status ? status : undefined,
      settings: Object.keys(changedSettings).length ? changedSettings : undefined,
    };
    if (body.name === undefined && body.status === undefined && body.settings === undefined) return;
    const { data } = await organizationApi.updateTenant(tenant.id, body);
    onChange(data);
  });

  const reseed = useAction(() => organizationApi.reseedRoles(tenant.id));

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (await save.run()) toast.success(t('orgs.detail.saved'));
  };

  return (
    <div className="cf-stack">
      <Card>
        <DescriptionList
          items={[
            {
              label: t('common.status'),
              value: (
                <Badge tone={tenantStatusTone(tenant.status)}>
                  {tEnum('orgs.status', tenant.status)}
                </Badge>
              ),
            },
            {
              label: t('orgs.detail.cellId'),
              value: <span className="cf-mono">{tenant.cell_id}</span>,
            },
            { label: t('common.createdAt'), value: formatDateTime(tenant.created_at) },
            { label: t('common.updatedAt'), value: formatDateTime(tenant.updated_at) },
            { label: t('common.id'), value: <span className="cf-mono">{tenant.id}</span> },
          ]}
        />
      </Card>
      <Card
        title={t('orgs.detail.settingsTitle')}
        actions={
          can(...PERMISSIONS.tenants.update) ? (
            <Button
              loading={reseed.busy}
              title={t('orgs.reseedRolesHint')}
              onClick={() => {
                void reseed.run().then((ok) => {
                  if (ok) toast.success(t('orgs.reseeded'));
                  else toast.error(errorMessage(reseed.error));
                });
              }}
            >
              {t('orgs.reseedRoles')}
            </Button>
          ) : null
        }
      >
        <form className="cf-form" onSubmit={(e) => void submit(e)} noValidate>
          <div className="cf-form__row">
            <FormField label={t('orgs.form.name')} htmlFor="org-edit-name" required>
              <Input
                id="org-edit-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                disabled={!editable}
              />
            </FormField>
            <FormField
              label={t('orgs.detail.statusTitle')}
              htmlFor="org-edit-status"
              hint={t('orgs.detail.statusHint')}
            >
              <Select
                id="org-edit-status"
                options={TENANT_STATUSES.map((s) => ({ value: s, label: tEnum('orgs.status', s) }))}
                value={status}
                onChange={(e) => setStatus(e.target.value as TenantStatus)}
                disabled={!editable}
              />
            </FormField>
          </div>
          <div className="cf-form__row">
            {TENANT_SETTING_KEYS.map((key) => (
              <FormField key={key} label={settingLabel(key)} htmlFor={`org-setting-${key}`}>
                <Input
                  id={`org-setting-${key}`}
                  value={settings[key]}
                  onChange={(e) => setSettings((s) => ({ ...s, [key]: e.target.value }))}
                  disabled={!editable}
                />
              </FormField>
            ))}
          </div>
          {save.error ? (
            <div className="cf-form__error" role="alert">
              {errorMessage(save.error)}
            </div>
          ) : null}
          {editable ? (
            <div className="cf-form__actions">
              {can(...PERMISSIONS.tenants.delete) ? (
                <Button
                  variant="danger"
                  icon={<IconTrash size={16} />}
                  onClick={() => setDeleting(true)}
                >
                  {t('orgs.delete')}
                </Button>
              ) : null}
              <Button type="submit" variant="primary" loading={save.busy}>
                {t('common.save')}
              </Button>
            </div>
          ) : null}
        </form>
      </Card>
      <ConfirmDialog
        open={deleting}
        title={t('orgs.delete')}
        message={t('orgs.deleteConfirm', { name: tenant.name })}
        confirmLabel={t('common.delete')}
        danger
        onCancel={() => setDeleting(false)}
        onConfirm={async () => {
          await organizationApi.deleteTenant(tenant.id);
          toast.success(t('orgs.deleted'));
          navigate(paths.organizations, { replace: true });
        }}
      />
    </div>
  );
}

function readSettings(tenant: Tenant): Record<TenantSettingKey, string> {
  const out = {} as Record<TenantSettingKey, string>;
  for (const key of TENANT_SETTING_KEYS) out[key] = tenant[key] ?? '';
  return out;
}

function MigrationsTab({ tenantId }: { tenantId: string }) {
  const { can } = useAccess();
  const status = useQuery(async () => (await organizationApi.migrationsStatus()).data.tenants, []);
  const rows = (status.data ?? []).filter((m) => m.tenant_id === tenantId);

  return (
    <Card title={t('orgs.migrations.forTenant')} flush>
      {status.data && rows.length === 0 ? (
        <EmptyState title={t('orgs.migrations.notListed')} />
      ) : (
        <MigrationsTable
          rows={rows}
          loading={status.loading}
          error={status.error}
          onRetry={status.reload}
          onMigrated={status.reload}
          canRun={can(...PERMISSIONS.migrations.run)}
          showTenant={false}
        />
      )}
    </Card>
  );
}
