import { useState } from 'react';
import {
  billingApi,
  billingMeta,
  type Plan,
  type PlanStatus,
  type Subscription,
  type SubscriptionStatus,
} from '@/api/billing';
import type { Tenant } from '@/api/organization';
import { usePagination } from '@/hooks/usePagination';
import { useQuery } from '@/hooks/useQuery';
import { useResource } from '@/hooks/useResource';
import { useTabParam } from '@/hooks/useTabParam';
import {
  Button,
  Card,
  ConfirmDialog,
  DataTable,
  ErrorState,
  Modal,
  PageHeader,
  Select,
  Skeleton,
  Tabs,
  useToast,
  type Column,
} from '@/design/components';
import { IconPlus } from '@/design/icons';
import { formatDateTime } from '@/lib/format';
import { t, tEnum } from '@/i18n';
import { ResourceGate } from '@/pages/shared/ResourceGate';
import { RowActions } from '@/pages/shared/RowActions';
import { formatMoney, formatPeriodDay, UsageTable } from './billingFormat';
import { LimitsCard } from './PlanPage';
import { PlanForm } from './PlanForm';
import { PlanStatusBadge, SubscriptionStatusBadge } from './subscriptionStatus';
import { SubscriptionForm } from './SubscriptionForm';
import { tenantLabel, useTenantDirectory } from './useTenantNames';

type TabId = 'plans' | 'subscriptions';

/** Catalogo de planes y suscripciones de todas las empresas: solo el superadmin. */
export default function PlatformBillingPage() {
  const tabs: { id: TabId; label: string }[] = [
    { id: 'plans', label: t('billing.platform.tab.plans') },
    { id: 'subscriptions', label: t('billing.platform.tab.subscriptions') },
  ];
  const [tab, setTab] = useTabParam(
    tabs.map((item) => item.id),
    'plans',
  );
  return (
    <div>
      <PageHeader
        title={t('billing.platform.title')}
        description={t('billing.platform.subtitle')}
      />
      <Tabs items={tabs} value={tab} onChange={setTab} label={t('billing.platform.title')} />
      {tab === 'plans' ? <PlansTab /> : <SubscriptionsTab />}
    </div>
  );
}

function PlansTab() {
  const toast = useToast();
  const [status, setStatus] = useState<PlanStatus | ''>('');
  const [editing, setEditing] = useState<Plan | null | undefined>(undefined);
  const [retiring, setRetiring] = useState<Plan | null>(null);
  const [viewing, setViewing] = useState<Plan | null>(null);
  const plans = useQuery(() => billingApi.listPlans(status || undefined), [status]);
  const meta = useResource(billingMeta);

  const columns: Column<Plan>[] = [
    {
      key: 'name',
      header: t('common.name'),
      render: (p) => (
        <div className="cf-cell-stack">
          <strong>{p.name}</strong>
          <span className="cf-mono cf-text-sm">{p.code}</span>
        </div>
      ),
    },
    {
      key: 'price',
      header: t('billing.price'),
      align: 'right',
      render: (p) =>
        t('billing.pricePer', {
          price: formatMoney(p.base_price, p.currency),
          period: tEnum('billing.periodUnit', p.billing_period),
        }),
    },
    {
      key: 'status',
      header: t('common.status'),
      render: (p) => <PlanStatusBadge status={p.status} />,
    },
    { key: 'updated', header: t('common.updatedAt'), render: (p) => formatDateTime(p.updated_at) },
    {
      key: 'actions',
      header: '',
      align: 'right',
      render: (p) => (
        <RowActions onEdit={() => setEditing(p)}>
          <Button size="sm" variant="ghost" onClick={() => setViewing(p)}>
            {t('billing.plans.viewLimits')}
          </Button>
          {p.status === 'active' ? (
            <Button size="sm" variant="ghost" onClick={() => setRetiring(p)}>
              {t('billing.plans.retire')}
            </Button>
          ) : null}
        </RowActions>
      ),
    },
  ];

  return (
    <Card
      flush
      title={t('billing.plans.title')}
      description={t('billing.plans.description')}
      actions={
        <Button variant="primary" icon={<IconPlus size={16} />} onClick={() => setEditing(null)}>
          {t('billing.plans.new')}
        </Button>
      }
    >
      <div className="cf-toolbar">
        <div className="cf-field">
          <label className="cf-field__label" htmlFor="plans-status">
            {t('common.status')}
          </label>
          <Select
            id="plans-status"
            placeholder={t('common.all')}
            options={(meta.data?.plan_statuses ?? []).map((s) => ({
              value: s,
              label: tEnum('billing.planStatus', s),
            }))}
            value={status}
            onChange={(e) => setStatus(e.target.value as PlanStatus | '')}
          />
        </div>
      </div>
      <DataTable
        columns={columns}
        rows={plans.data ?? []}
        rowKey={(p) => p.id}
        loading={plans.loading}
        error={plans.error}
        onRetry={plans.reload}
        empty={{ title: t('billing.plans.empty') }}
      />
      {editing !== undefined ? (
        <PlanForm
          plan={editing}
          onClose={() => setEditing(undefined)}
          onSaved={() => {
            toast.success(editing ? t('billing.plans.updated') : t('billing.plans.created'));
            setEditing(undefined);
            plans.reload();
          }}
        />
      ) : null}
      {viewing ? (
        <Modal
          open
          size="lg"
          title={viewing.name}
          onClose={() => setViewing(null)}
          footer={<Button onClick={() => setViewing(null)}>{t('common.close')}</Button>}
        >
          <ResourceGate resource={meta}>
            {(catalog) => <LimitsCard plan={viewing} unlimited={catalog.unlimited} />}
          </ResourceGate>
        </Modal>
      ) : null}
      <ConfirmDialog
        open={retiring !== null}
        title={t('billing.plans.retire')}
        message={t('billing.plans.retireConfirm', { name: retiring?.name ?? '' })}
        confirmLabel={t('billing.plans.retire')}
        danger
        onCancel={() => setRetiring(null)}
        onConfirm={async () => {
          if (!retiring) return;
          await billingApi.retirePlan(retiring.id);
          toast.success(t('billing.plans.retired'));
          setRetiring(null);
          plans.reload();
        }}
      />
    </Card>
  );
}

function SubscriptionsTab() {
  const toast = useToast();
  const pager = usePagination();
  const directory = useTenantDirectory();
  const [status, setStatus] = useState<SubscriptionStatus | ''>('');
  const [editing, setEditing] = useState<Subscription | null | undefined>(undefined);
  const [usageOf, setUsageOf] = useState<Subscription | null>(null);
  const subscriptions = useQuery(
    () =>
      billingApi.listSubscriptions({
        page: pager.page,
        per_page: pager.perPage,
        status: status || undefined,
      }),
    [pager.page, pager.perPage, status],
  );
  const plans = useQuery(() => billingApi.listPlans(), []);
  const meta = useResource(billingMeta);

  const columns: Column<Subscription>[] = [
    {
      key: 'tenant',
      header: t('billing.subscriptions.tenant'),
      render: (s) => <span className="cf-break">{tenantLabel(directory.byId, s.tenant_id)}</span>,
    },
    {
      key: 'plan',
      header: t('billing.plan'),
      render: (s) => <span className="cf-mono">{s.plan_code}</span>,
    },
    {
      key: 'status',
      header: t('common.status'),
      render: (s) => <SubscriptionStatusBadge status={s.status} />,
    },
    {
      key: 'period',
      header: t('billing.period'),
      render: (s) =>
        t('billing.periodRange', {
          from: formatPeriodDay(s.current_period_start),
          to: formatPeriodDay(s.current_period_end),
        }),
    },
    {
      key: 'trial',
      header: t('billing.trialEnds'),
      render: (s) => formatDateTime(s.trial_ends_at),
    },
    { key: 'cancel', header: t('billing.cancelAt'), render: (s) => formatDateTime(s.cancel_at) },
    {
      key: 'actions',
      header: '',
      align: 'right',
      render: (s) => (
        <RowActions>
          <Button size="sm" variant="ghost" onClick={() => setUsageOf(s)}>
            {t('billing.subscriptions.usage')}
          </Button>
          <Button size="sm" variant="ghost" onClick={() => setEditing(s)}>
            {t('billing.subscriptions.change')}
          </Button>
        </RowActions>
      ),
    },
  ];

  return (
    <Card
      flush
      title={t('billing.subscriptions.title')}
      description={t('billing.subscriptions.description')}
      actions={
        <Button
          variant="primary"
          icon={<IconPlus size={16} />}
          onClick={() => setEditing(null)}
          disabled={!plans.data}
        >
          {t('billing.subscriptions.assign')}
        </Button>
      }
    >
      <div className="cf-toolbar">
        <div className="cf-field">
          <label className="cf-field__label" htmlFor="subscriptions-status">
            {t('common.status')}
          </label>
          <Select
            id="subscriptions-status"
            placeholder={t('common.all')}
            options={(meta.data?.subscription_statuses ?? []).map(({ status: s }) => ({
              value: s,
              label: tEnum('billing.subscriptionStatus', s),
            }))}
            value={status}
            onChange={(e) => {
              setStatus(e.target.value as SubscriptionStatus | '');
              pager.reset();
            }}
          />
        </div>
      </div>
      <DataTable
        columns={columns}
        rows={subscriptions.data?.items ?? []}
        rowKey={(s) => s.id}
        loading={subscriptions.loading}
        error={subscriptions.error}
        onRetry={subscriptions.reload}
        empty={{ title: t('billing.subscriptions.empty') }}
        pagination={{
          page: subscriptions.data?.page ?? pager.page,
          perPage: pager.perPage,
          total: subscriptions.data?.total ?? 0,
          totalPages: subscriptions.data?.totalPages ?? 0,
          onPageChange: pager.setPage,
        }}
      />
      {editing !== undefined && plans.data ? (
        <SubscriptionForm
          subscription={editing}
          tenantId={editing?.tenant_id ?? null}
          tenants={directory.tenants}
          plans={plans.data}
          onClose={() => setEditing(undefined)}
          onSaved={() => {
            toast.success(t('billing.subscriptions.saved'));
            setEditing(undefined);
            subscriptions.reload();
          }}
        />
      ) : null}
      {usageOf ? (
        <TenantUsageModal
          subscription={usageOf}
          byId={directory.byId}
          onClose={() => setUsageOf(null)}
        />
      ) : null}
    </Card>
  );
}

function TenantUsageModal({
  subscription,
  byId,
  onClose,
}: {
  subscription: Subscription;
  byId: Map<string, Tenant>;
  onClose: () => void;
}) {
  const usage = useQuery(
    async () => (await billingApi.tenantUsage(subscription.tenant_id)).data,
    [subscription.tenant_id],
  );
  const meta = useResource(billingMeta);
  return (
    <Modal
      open
      size="lg"
      title={t('billing.subscriptions.usageTitle', {
        tenant: tenantLabel(byId, subscription.tenant_id),
      })}
      onClose={onClose}
      footer={<Button onClick={onClose}>{t('common.close')}</Button>}
    >
      {usage.error ? (
        <ErrorState error={usage.error} onRetry={usage.reload} />
      ) : meta.error ? (
        <ErrorState error={meta.error} onRetry={meta.reload} />
      ) : !usage.data || !meta.data ? (
        <Skeleton lines={6} />
      ) : (
        <div className="cf-stack" style={{ gap: 'var(--cf-space-3)' }}>
          <span className="cf-text-sm cf-text-secondary">
            {t('billing.periodRange', {
              from: formatPeriodDay(usage.data.period_start),
              to: formatPeriodDay(usage.data.period_end),
            })}
          </span>
          <UsageTable report={usage.data} unlimited={meta.data.unlimited} />
        </div>
      )}
    </Modal>
  );
}
